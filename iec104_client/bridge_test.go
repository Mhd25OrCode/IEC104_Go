package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

type sentCmd struct {
	name  string
	ca    uint16
	ioa   asdu.InfoObjAddr
	value any
}

type fakeClient struct {
	mu   sync.Mutex
	sent []sentCmd
	err  error
}

func (f *fakeClient) record(c sentCmd) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, c)
	return f.err
}

func (f *fakeClient) SendCmd(addr uint16, _ asdu.TypeID, ioa asdu.InfoObjAddr, value any) error {
	return f.record(sentCmd{"cmd", addr, ioa, value})
}
func (f *fakeClient) SendInterrogationCmd(addr uint16) error {
	return f.record(sentCmd{name: "interrogation", ca: addr})
}
func (f *fakeClient) SendCounterInterrogationCmd(addr uint16) error {
	return f.record(sentCmd{name: "counter", ca: addr})
}
func (f *fakeClient) SendReadCmd(addr uint16, ioa uint) error {
	return f.record(sentCmd{"read", addr, asdu.InfoObjAddr(ioa), nil})
}
func (f *fakeClient) SendClockSynchronizationCmd(addr uint16, _ time.Time) error {
	return f.record(sentCmd{name: "clock", ca: addr})
}
func (f *fakeClient) SendTestCmd(addr uint16) error {
	return f.record(sentCmd{name: "test", ca: addr})
}
func (f *fakeClient) SendResetProcessCmd(addr uint16) error {
	return f.record(sentCmd{name: "reset", ca: addr})
}

// harness wires a bridge to io.Pipe stdin/stdout, like the backend does.
type harness struct {
	b        *bridge
	fake     *fakeClient
	stdinW   *io.PipeWriter
	stdoutR  *bufio.Reader
	serveErr chan error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := &fakeClient{}
	b := newBridge(fake)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	b.start(stdoutW)
	serveErr := make(chan error, 1)
	go func() { serveErr <- b.serve(stdinR) }()
	t.Cleanup(func() { stdinW.Close() })
	return &harness{b: b, fake: fake, stdinW: stdinW, stdoutR: bufio.NewReader(stdoutR), serveErr: serveErr}
}

func (h *harness) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(h.stdinW, line+"\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
}

// next reads one stdout line and requires it to be a valid JSON object.
func (h *harness) next(t *testing.T) map[string]any {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := h.stdoutR.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read stdout: %v", r.err)
		}
		if !strings.HasSuffix(r.line, "\n") {
			t.Fatalf("line not newline-terminated: %q", r.line)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(r.line), &m); err != nil {
			t.Fatalf("stdout line is not valid JSON: %q: %v", r.line, err)
		}
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a stdout line")
		return nil
	}
}

func TestReadyIsFirstLine(t *testing.T) {
	h := newHarness(t)
	m := h.next(t)
	if m["op"] != "ready" || m["version"] != float64(1) {
		t.Fatalf("unexpected first frame: %v", m)
	}
}

func TestDispatchAck(t *testing.T) {
	h := newHarness(t)
	h.next(t) // ready

	h.send(t, `{"op":"dispatch","reqId":"7","type":"OUT","points":[`+
		`{"id":"a","type":"OUT","asduTyp":"C_SC_NA_1","asduHigh":null,"asduLow":1,"ioaHigh":null,"ioaMiddle":1,"ioaLow":5,"payload":{"value":true}},`+
		`{"id":"b","asduTyp":"C_IC_NA_1","asduLow":1}]}`)
	ack := h.next(t)
	if ack["op"] != "ack" || ack["reqId"] != "7" || ack["accepted"] != true || ack["message"] != "" {
		t.Fatalf("unexpected ack: %v", ack)
	}
	if len(h.fake.sent) != 2 {
		t.Fatalf("expected 2 commands, got %+v", h.fake.sent)
	}
	if got := h.fake.sent[0]; got.name != "cmd" || got.ca != 1 || got.ioa != 0x105 || got.value != true {
		t.Fatalf("unexpected single command: %+v", got)
	}
	if got := h.fake.sent[1]; got.name != "interrogation" || got.ca != 1 {
		t.Fatalf("unexpected interrogation: %+v", got)
	}
}

func TestDispatchAcceptsMnemonicWithTypeIDSuffix(t *testing.T) {
	h := newHarness(t)
	h.next(t) // ready

	// MSL5 sends "<mnemonic>_<typeID>" (45 is C_SC_NA_1's type id); the bridge
	// must dispatch it as the same single command as the bare mnemonic.
	h.send(t, `{"op":"dispatch","reqId":"11","type":"OUT","points":[`+
		`{"id":"z","asduTyp":"C_SC_NA_1_45","asduLow":1,"ioaLow":9,"payload":{"value":true}}]}`)
	ack := h.next(t)
	if ack["reqId"] != "11" || ack["accepted"] != true || ack["message"] != "" {
		t.Fatalf("unexpected ack: %v", ack)
	}
	if got := h.fake.sent[0]; got.name != "cmd" || got.ca != 1 || got.ioa != 9 || got.value != true {
		t.Fatalf("unexpected single command: %+v", got)
	}
}

func TestDispatchRejectedAck(t *testing.T) {
	h := newHarness(t)
	h.next(t)

	h.send(t, `{"op":"dispatch","reqId":"8","type":"OUT","points":[{"id":"x","asduTyp":"X_FOO_1"}]}`)
	ack := h.next(t)
	if ack["reqId"] != "8" || ack["accepted"] != false || !strings.Contains(ack["message"].(string), "X_FOO_1") {
		t.Fatalf("unexpected ack: %v", ack)
	}

	h.fake.err = errors.New("not connected")
	h.send(t, `{"op":"dispatch","reqId":"9","type":"OUT","points":[{"id":"y","asduTyp":"C_IC_NA_1"}]}`)
	ack = h.next(t)
	if ack["reqId"] != "9" || ack["accepted"] != false || !strings.Contains(ack["message"].(string), "not connected") {
		t.Fatalf("unexpected ack: %v", ack)
	}
}

// TestDispatchMissingCommandValueIsRejected covers the IEC104_GO_BRIDGE_SYNC.md
// guarantee: the backend always sends payload.value for command points (and
// omits points that have none). If a command point arrives without one, the
// bridge must reject it per-point and send nothing - never default the missing
// value to a false/0 command.
func TestDispatchMissingCommandValueIsRejected(t *testing.T) {
	h := newHarness(t)
	h.next(t) // ready

	h.send(t, `{"op":"dispatch","reqId":"12","type":"OUT","points":[`+
		`{"id":"m","type":"OUT","asduTyp":"C_SC_NA_1","asduLow":1,"ioaLow":5}]}`)
	ack := h.next(t)
	if ack["reqId"] != "12" || ack["accepted"] != false || !strings.Contains(ack["message"].(string), "payload.value") {
		t.Fatalf("unexpected ack: %v", ack)
	}
	if len(h.fake.sent) != 0 {
		t.Fatalf("command with a missing value must not be sent: %+v", h.fake.sent)
	}
}

func TestEventEchoesDispatchedPoint(t *testing.T) {
	h := newHarness(t)
	h.next(t)

	h.send(t, `{"op":"dispatch","reqId":"1","type":"OUT","points":[`+
		`{"id":"row-9","type":"OUT","designation":"Pump","channelId":"c1","channelName":"Chan","asduTyp":"C_SC_NA_1","asduLow":1,"ioaLow":5,"typ":"Wert","active":true,"createdAt":1700000000000,"payload":{"value":true}}]}`)
	h.next(t) // ack

	h.b.publishEvent(1, 5, asdu.C_SC_NA_1, asdu.CauseOfTransmission{Cause: asdu.ActivationCon}, true)
	ev := h.next(t)
	if ev["op"] != "event" {
		t.Fatalf("unexpected frame: %v", ev)
	}
	p := ev["point"].(map[string]any)
	if p["id"] != "row-9" || p["type"] != "OUT" || p["designation"] != "Pump" || p["channelId"] != "c1" ||
		p["asduTyp"] != "C_SC_NA_1" || p["asduHigh"] != float64(0) || p["asduLow"] != float64(1) ||
		p["ioaLow"] != float64(5) || p["createdAt"] != float64(1700000000000) {
		t.Fatalf("unexpected event point: %v", p)
	}
	payload := p["payload"].(map[string]any)
	if payload["value"] != true || !strings.Contains(payload["cot"].(string), "ActivationCon") {
		t.Fatalf("unexpected payload: %v", payload)
	}
}

func TestEventForUnknownPointInfersDirection(t *testing.T) {
	h := newHarness(t)
	h.next(t)

	h.b.publishEvent(0x0102, 0x010203, asdu.M_SP_NA_1, asdu.CauseOfTransmission{Cause: asdu.Spontaneous}, 1)
	p := h.next(t)["point"].(map[string]any)
	if p["type"] != "IN" || p["asduTyp"] != "M_SP_NA_1" || p["asduHigh"] != float64(1) || p["asduLow"] != float64(2) ||
		p["ioaHigh"] != float64(1) || p["ioaMiddle"] != float64(2) || p["ioaLow"] != float64(3) {
		t.Fatalf("unexpected event point: %v", p)
	}
	if _, has := p["id"]; has {
		t.Fatalf("unknown point must not carry an id: %v", p)
	}
}

func TestMalformedAndUnknownLinesAreIgnored(t *testing.T) {
	h := newHarness(t)
	h.next(t)

	h.send(t, `not json`)
	h.send(t, `{"op":"mystery"}`)
	h.send(t, ``)
	h.send(t, `{"op":"dispatch","reqId":"2","points":[]}`)
	ack := h.next(t)
	if ack["reqId"] != "2" || ack["accepted"] != true {
		t.Fatalf("bridge did not survive bad lines: %v", ack)
	}
}

func TestLargeBatchIsNotTruncated(t *testing.T) {
	h := newHarness(t)
	h.next(t)

	const n = 3000 // well past bufio.Scanner's 64 KB line limit
	var sb strings.Builder
	sb.WriteString(`{"op":"dispatch","reqId":"big","type":"IN","points":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":"p%d","designation":"a long designation to pad the line","asduTyp":"M_SP_NA_1","asduLow":1,"ioaLow":%d}`, i, i%256)
	}
	sb.WriteString(`]}`)
	if sb.Len() < 64*1024 {
		t.Fatalf("test line too short: %d", sb.Len())
	}
	go io.WriteString(h.stdinW, sb.String()+"\n")
	ack := h.next(t)
	if ack["reqId"] != "big" || ack["accepted"] != true {
		t.Fatalf("unexpected ack: %v", ack)
	}
}

func TestEOFEndsServeCleanlyAndFlushesFrames(t *testing.T) {
	h := newHarness(t)
	h.next(t)

	h.b.publishEvent(1, 1, asdu.M_SP_NA_1, asdu.CauseOfTransmission{Cause: asdu.Spontaneous}, 1)
	h.stdinW.Close()
	select {
	case err := <-h.serveErr:
		if err != nil {
			t.Fatalf("serve returned %v on EOF", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return on EOF")
	}
	if ev := h.next(t); ev["op"] != "event" {
		t.Fatalf("queued event lost on shutdown: %v", ev)
	}
	stopped := make(chan struct{})
	go func() { h.b.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not return")
	}
}

func TestPublishEventNeverBlocksWhenBufferFull(t *testing.T) {
	b := newBridge(&fakeClient{}) // no writer: the buffer fills up
	done := make(chan struct{})
	go func() {
		for i := 0; i < eventBuffer*2; i++ {
			b.publishEvent(1, 1, asdu.M_SP_NA_1, asdu.CauseOfTransmission{}, i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("publishEvent blocked on a full buffer")
	}
}

func TestNilBridgePublishEventIsSafe(t *testing.T) {
	var b *bridge
	b.publishEvent(1, 1, asdu.M_SP_NA_1, asdu.CauseOfTransmission{}, 1)
}
