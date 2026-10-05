package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

// protocolVersion is announced in the "ready" frame; the backend rejects any
// other value.
const protocolVersion = 1

// eventBuffer is the capacity of the outgoing frame channel. publishEvent runs
// on the IEC 104 library's handlerLoop goroutine and must never block, so
// frames are dropped once this buffer is full.
const eventBuffer = 1024

// point mirrors the backend's Iec104Dto (NDJSON "point" object). Numeric
// address parts are pointers because null means "not set" (0 is a valid value).
// Fields the bridge never interprets are kept as json.RawMessage so they can be
// echoed back untouched.
type point struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type,omitempty"`
	Designation string `json:"designation,omitempty"`
	ChannelID   string `json:"channelId,omitempty"`
	ChannelName string `json:"channelName,omitempty"`
	AsduTyp     string `json:"asduTyp,omitempty"`
	AsduHigh    *int   `json:"asduHigh"`
	AsduLow     *int   `json:"asduLow"`
	IoaHigh     *int   `json:"ioaHigh"`
	IoaMiddle   *int   `json:"ioaMiddle"`
	IoaLow      *int   `json:"ioaLow"`
	Typ         string `json:"typ,omitempty"`
	Active      *bool  `json:"active,omitempty"`

	Payload json.RawMessage `json:"payload,omitempty"`

	CreatedAt json.RawMessage `json:"createdAt,omitempty"`
	UpdatedAt json.RawMessage `json:"updatedAt,omitempty"`

	Intervall             json.RawMessage `json:"intervall,omitempty"`
	TransmissionThreshold json.RawMessage `json:"transmissionThreshold,omitempty"`
	AsduRetTyp            json.RawMessage `json:"asduRetTyp,omitempty"`
	IoaRetHigh            json.RawMessage `json:"ioaRetHigh,omitempty"`
	IoaRetMiddle          json.RawMessage `json:"ioaRetMiddle,omitempty"`
	IoaRetLow             json.RawMessage `json:"ioaRetLow,omitempty"`
}

// request is any line the backend sends on stdin.
type request struct {
	Op     string          `json:"op"`
	ReqID  json.RawMessage `json:"reqId"`
	Type   string          `json:"type"`
	Points []*point        `json:"points"`
}

type readyFrame struct {
	Op      string `json:"op"`
	Version int    `json:"version"`
}

type ackFrame struct {
	Op       string          `json:"op"`
	ReqID    json.RawMessage `json:"reqId"`
	Accepted bool            `json:"accepted"`
	Message  string          `json:"message"`
}

type eventFrame struct {
	Op    string `json:"op"`
	Point *point `json:"point"`
}

// pointKey correlates an inbound IEC 104 event to a previously dispatched
// point: IEC 104 has no concept of the backend row `id`, so
// (CommonAddr, InfoObjAddr) is the only stable join key available.
type pointKey struct {
	ca  asdu.CommonAddr
	ioa asdu.InfoObjAddr
}

// registry remembers the metadata (id, designation, channel, ...) of the last
// point dispatched for a given address, so that later spontaneous/reply events
// can be echoed back to the backend with the same identity.
type registry struct {
	mu     sync.RWMutex
	points map[pointKey]*point
}

func newRegistry() *registry {
	return &registry{points: make(map[pointKey]*point)}
}

func (r *registry) upsert(ca asdu.CommonAddr, ioa asdu.InfoObjAddr, p *point) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.points[pointKey{ca, ioa}] = p
}

func (r *registry) lookup(ca asdu.CommonAddr, ioa asdu.InfoObjAddr) *point {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.points[pointKey{ca, ioa}]
}

// commander is the subset of *client.Client the bridge drives; it exists so the
// protocol layer can be tested without an outstation.
type commander interface {
	SendCmd(addr uint16, typeId asdu.TypeID, ioa asdu.InfoObjAddr, value any) error
	SendInterrogationCmd(addr uint16) error
	SendCounterInterrogationCmd(addr uint16) error
	SendReadCmd(addr uint16, ioa uint) error
	SendClockSynchronizationCmd(addr uint16, t time.Time) error
	SendTestCmd(addr uint16) error
	SendResetProcessCmd(addr uint16) error
}

// bridge speaks the NDJSON protocol: requests arrive on an io.Reader (stdin),
// every outgoing frame (ready, ack, event) goes through one channel to a single
// writer goroutine that owns the io.Writer (stdout), so lines never interleave.
type bridge struct {
	client commander
	reg    *registry

	out      chan any
	done     chan struct{}
	writerWG sync.WaitGroup
}

func newBridge(c commander) *bridge {
	return &bridge{
		client: c,
		reg:    newRegistry(),
		out:    make(chan any, eventBuffer),
		done:   make(chan struct{}),
	}
}

// setClient wires the IEC 104 client in once it exists (it needs the
// masterCall, which in turn needs the bridge).
func (b *bridge) setClient(c commander) { b.client = c }

// start launches the writer goroutine on w and queues the "ready" frame.
func (b *bridge) start(w io.Writer) {
	b.writerWG.Add(1)
	go b.writeLoop(w)
	b.out <- readyFrame{Op: "ready", Version: protocolVersion}
}

// stop flushes every frame queued so far and ends the writer goroutine.
func (b *bridge) stop() {
	close(b.done)
	b.writerWG.Wait()
}

func (b *bridge) writeLoop(w io.Writer) {
	defer b.writerWG.Done()
	bw := bufio.NewWriter(w)
	write := func(frame any) {
		data, err := json.Marshal(frame)
		if err != nil {
			log.Printf("[bridge] cannot encode frame: %v", err)
			return
		}
		data = append(data, '\n')
		if _, err := bw.Write(data); err != nil {
			log.Printf("[bridge] stdout write: %v", err)
		}
	}
	for {
		select {
		case frame := <-b.out:
			write(frame)
			// Batch whatever is already queued into a single flush.
			for drained := false; !drained; {
				select {
				case frame := <-b.out:
					write(frame)
				default:
					drained = true
				}
			}
			if err := bw.Flush(); err != nil {
				log.Printf("[bridge] stdout flush: %v", err)
			}
		case <-b.done:
			for {
				select {
				case frame := <-b.out:
					write(frame)
				default:
					if err := bw.Flush(); err != nil {
						log.Printf("[bridge] stdout flush: %v", err)
					}
					return
				}
			}
		}
	}
}

// serve reads NDJSON requests until EOF. It uses bufio.Reader.ReadBytes rather
// than bufio.Scanner so lines of any length (a "Lesen" batch with many points)
// are never truncated. A clean EOF returns nil.
func (b *bridge) serve(in io.Reader) error {
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			b.handleLine(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (b *bridge) handleLine(line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		log.Printf("[bridge] ignoring malformed line: %v", err)
		return
	}
	switch req.Op {
	case "dispatch":
		b.out <- b.dispatch(&req)
	default:
		log.Printf("[bridge] ignoring unknown op %q", req.Op)
	}
}

// dispatch sends one IEC 104 command per point, driven entirely by each
// point's own asduTyp (not by the batch-level type). Errors are collected per
// point into a single accepted:false ack instead of failing the whole batch.
func (b *bridge) dispatch(req *request) ackFrame {
	var failures []string
	for _, p := range req.Points {
		if p == nil {
			continue
		}
		if err := b.dispatchPoint(p); err != nil {
			failures = append(failures, fmt.Sprintf("%s(%s): %v", p.AsduTyp, p.ID, err))
		}
	}
	ack := ackFrame{Op: "ack", ReqID: req.ReqID, Accepted: len(failures) == 0}
	if len(failures) > 0 {
		ack.Message = strings.Join(failures, "; ")
	}
	return ack
}

func (b *bridge) dispatchPoint(p *point) error {
	typeId, ok := typeIDFromName(p.AsduTyp)
	if !ok {
		return fmt.Errorf("unknown asduTyp %q", p.AsduTyp)
	}
	ca := commonAddrFromPoint(p)
	ioa := infoObjAddrFromPoint(p)
	b.reg.upsert(ca, ioa, p)

	switch typeId {
	case asdu.C_IC_NA_1:
		return b.client.SendInterrogationCmd(uint16(ca))
	case asdu.C_CI_NA_1:
		return b.client.SendCounterInterrogationCmd(uint16(ca))
	case asdu.C_RD_NA_1:
		return b.client.SendReadCmd(uint16(ca), uint(ioa))
	case asdu.C_CS_NA_1:
		return b.client.SendClockSynchronizationCmd(uint16(ca), time.Now())
	case asdu.C_TS_TA_1:
		return b.client.SendTestCmd(uint16(ca))
	case asdu.C_RP_NA_1:
		return b.client.SendResetProcessCmd(uint16(ca))
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1, asdu.C_DC_NA_1, asdu.C_DC_TA_1,
		asdu.C_RC_NA_1, asdu.C_RC_TA_1, asdu.C_SE_NA_1, asdu.C_SE_TA_1,
		asdu.C_SE_NB_1, asdu.C_SE_TB_1, asdu.C_SE_NC_1, asdu.C_SE_TC_1,
		asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		value, err := commandValueFromPayload(typeId, p.Payload)
		if err != nil {
			return err
		}
		return b.client.SendCmd(uint16(ca), typeId, ioa, value)
	default:
		// Monitoring types (M_*) aren't sendable commands; the point is still
		// registered above so later spontaneous events can echo it back.
		return nil
	}
}

// publishEvent builds the outgoing point for one decoded IEC 104 event and
// queues it; masterCall calls this from every callback on the library's
// handlerLoop goroutine, so it never blocks: when the buffer is full the frame
// is dropped. Safe to call on a nil *bridge (e.g. with -demo).
func (b *bridge) publishEvent(ca asdu.CommonAddr, ioa asdu.InfoObjAddr, typeId asdu.TypeID, cot asdu.CauseOfTransmission, value any) {
	if b == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"value": value,
		"cot":   fmt.Sprintf("%v", cot),
	})
	if err != nil {
		payload = []byte("{}")
	}

	typeName := typeNameFromID(typeId)
	p := &point{
		Type:      directionFromTypeName(typeName),
		AsduTyp:   typeName,
		AsduHigh:  intPtr(int(ca >> 8)),
		AsduLow:   intPtr(int(ca & 0xFF)),
		IoaHigh:   intPtr(int((ioa >> 16) & 0xFF)),
		IoaMiddle: intPtr(int((ioa >> 8) & 0xFF)),
		IoaLow:    intPtr(int(ioa & 0xFF)),
		Payload:   payload,
		UpdatedAt: json.RawMessage(fmt.Sprintf("%d", time.Now().UnixMilli())),
	}
	if known := b.reg.lookup(ca, ioa); known != nil {
		p.ID = known.ID
		if known.Type != "" {
			p.Type = known.Type
		}
		p.Designation = known.Designation
		p.ChannelID = known.ChannelID
		p.ChannelName = known.ChannelName
		p.Typ = known.Typ
		p.Active = known.Active
		p.CreatedAt = known.CreatedAt
	}

	select {
	case b.out <- eventFrame{Op: "event", Point: p}:
	default:
		log.Printf("[bridge] event buffer full, dropping frame")
	}
}

// directionFromTypeName guesses the backend row direction for events of points
// that were never dispatched: monitoring types (M_*) are inputs, commands
// (C_*) are outputs.
func directionFromTypeName(name string) string {
	if strings.HasPrefix(name, "C_") {
		return "OUT"
	}
	return "IN"
}

func intPtr(v int) *int { return &v }

func intOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// commonAddrFromPoint reassembles the 2-byte Common Address split across
// asduHigh/asduLow (big-endian: high byte first).
func commonAddrFromPoint(p *point) asdu.CommonAddr {
	return asdu.CommonAddr(uint16(intOrZero(p.AsduHigh))<<8 | uint16(intOrZero(p.AsduLow))&0xFF)
}

// infoObjAddrFromPoint reassembles the 3-byte Information Object Address split
// across ioaHigh/ioaMiddle/ioaLow (big-endian: high byte first).
func infoObjAddrFromPoint(p *point) asdu.InfoObjAddr {
	return asdu.InfoObjAddr(uint32(intOrZero(p.IoaHigh))<<16 | uint32(intOrZero(p.IoaMiddle))<<8 | uint32(intOrZero(p.IoaLow))&0xFF)
}
