package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/iec104/client"
	"google.golang.org/grpc"

	"github.com/alfas/iec104-client/pb"
)

// pointKey correlates an inbound IEC 104 event to a previously dispatched
// Iec104Point: IEC 104 has no concept of the gRPC contract's `id`, so
// (CommonAddr, InfoObjAddr) is the only stable join key available.
type pointKey struct {
	ca  asdu.CommonAddr
	ioa asdu.InfoObjAddr
}

// registry remembers the metadata (id, designation, channel, ...) of the last
// Iec104Point dispatched for a given point, so that later spontaneous/reply
// events can be echoed back to NestJS with the same identity.
type registry struct {
	mu     sync.RWMutex
	points map[pointKey]*pb.Iec104Point
}

func newRegistry() *registry {
	return &registry{points: make(map[pointKey]*pb.Iec104Point)}
}

func (r *registry) upsert(ca asdu.CommonAddr, ioa asdu.InfoObjAddr, p *pb.Iec104Point) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.points[pointKey{ca, ioa}] = p
}

func (r *registry) lookup(ca asdu.CommonAddr, ioa asdu.InfoObjAddr) *pb.Iec104Point {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.points[pointKey{ca, ioa}]
}

// eventHub fans a decoded IEC 104 event out to every SubscribeEvents caller.
type eventHub struct {
	mu   sync.RWMutex
	subs map[uint64]chan *pb.Iec104Point
	next uint64
}

func newEventHub() *eventHub {
	return &eventHub{subs: make(map[uint64]chan *pb.Iec104Point)}
}

func (h *eventHub) subscribe() (id uint64, ch chan *pb.Iec104Point) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id = h.next
	ch = make(chan *pb.Iec104Point, 256)
	h.subs[id] = ch
	return id, ch
}

func (h *eventHub) unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, id)
}

// publish fans p out to every subscriber without blocking the caller: masterCall's
// callbacks run on the IEC 104 library's single handlerLoop goroutine (see
// docs/iec104_client_architecture.md §2.3), so a slow/stuck gRPC subscriber must
// never be allowed to stall protocol processing.
func (h *eventHub) publish(p *pb.Iec104Point) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, ch := range h.subs {
		select {
		case ch <- p:
		default:
			log.Printf("[grpc] subscriber %d event buffer full, dropping frame", id)
		}
	}
}

// bridgeServer is the adapter between the gRPC Iec104Bridge contract and the
// existing IEC 104 client/event architecture: Dispatch drives the shared
// *client.Client, and publishEvent (called from masterCall) feeds the hub that
// SubscribeEvents streams from.
type bridgeServer struct {
	pb.UnimplementedIec104BridgeServer
	client *client.Client
	hub    *eventHub
	reg    *registry
}

func newBridgeServer(c *client.Client) *bridgeServer {
	return &bridgeServer{client: c, hub: newEventHub(), reg: newRegistry()}
}

// Dispatch sends one IEC 104 command per point, driven entirely by each
// point's own asdu_typ (not by the batch-level DispatchRequest.type). Errors
// are collected per-point instead of failing the whole RPC.
func (s *bridgeServer) Dispatch(_ context.Context, req *pb.DispatchRequest) (*pb.DispatchAck, error) {
	var failures []string
	for _, p := range req.GetPoints() {
		if err := s.dispatchPoint(p); err != nil {
			failures = append(failures, fmt.Sprintf("%s(%s): %v", p.GetAsduTyp(), p.GetId(), err))
		}
	}
	if len(failures) > 0 {
		return &pb.DispatchAck{Accepted: false, Message: strings.Join(failures, "; ")}, nil
	}
	return &pb.DispatchAck{Accepted: true}, nil
}

func (s *bridgeServer) dispatchPoint(p *pb.Iec104Point) error {
	typeId, ok := typeIDFromName(p.GetAsduTyp())
	if !ok {
		return fmt.Errorf("unknown asdu_typ %q", p.GetAsduTyp())
	}
	ca := commonAddrFromPoint(p)
	ioa := infoObjAddrFromPoint(p)
	s.reg.upsert(ca, ioa, p)

	switch typeId {
	case asdu.C_IC_NA_1:
		return s.client.SendInterrogationCmd(uint16(ca))
	case asdu.C_CI_NA_1:
		return s.client.SendCounterInterrogationCmd(uint16(ca))
	case asdu.C_RD_NA_1:
		return s.client.SendReadCmd(uint16(ca), uint(ioa))
	case asdu.C_CS_NA_1:
		return s.client.SendClockSynchronizationCmd(uint16(ca), time.Now())
	case asdu.C_TS_TA_1:
		return s.client.SendTestCmd(uint16(ca))
	case asdu.C_RP_NA_1:
		return s.client.SendResetProcessCmd(uint16(ca))
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1, asdu.C_DC_NA_1, asdu.C_DC_TA_1,
		asdu.C_RC_NA_1, asdu.C_RC_TA_1, asdu.C_SE_NA_1, asdu.C_SE_TA_1,
		asdu.C_SE_NB_1, asdu.C_SE_TB_1, asdu.C_SE_NC_1, asdu.C_SE_TC_1,
		asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		value, err := commandValueFromPayload(typeId, p.GetPayloadJson())
		if err != nil {
			return err
		}
		return s.client.SendCmd(uint16(ca), typeId, ioa, value)
	default:
		// Monitoring types (M_*) aren't sendable commands; the point is still
		// registered above so later spontaneous events can echo it back.
		return nil
	}
}

// SubscribeEvents streams every point published via publishEvent to one
// caller for as long as its context stays open.
func (s *bridgeServer) SubscribeEvents(_ *pb.SubscribeRequest, stream grpc.ServerStreamingServer[pb.Iec104Point]) error {
	id, ch := s.hub.subscribe()
	defer s.hub.unsubscribe(id)
	log.Printf("[grpc] subscriber %d connected", id)
	for {
		select {
		case <-stream.Context().Done():
			log.Printf("[grpc] subscriber %d disconnected", id)
			return nil
		case p := <-ch:
			if err := stream.Send(p); err != nil {
				log.Printf("[grpc] subscriber %d send error: %v", id, err)
				return err
			}
		}
	}
}

// publishEvent builds the outgoing Iec104Point for one decoded IEC 104 event
// and fans it out via the hub; masterCall calls this from every callback.
// Safe to call on a nil *bridgeServer (e.g. when no gRPC layer is wired up).
func (s *bridgeServer) publishEvent(ca asdu.CommonAddr, ioa asdu.InfoObjAddr, typeId asdu.TypeID, cot asdu.CauseOfTransmission, value any) {
	if s == nil {
		return
	}
	payloadJSON, err := json.Marshal(map[string]any{
		"value": value,
		"cot":   fmt.Sprintf("%v", cot),
	})
	if err != nil {
		payloadJSON = []byte("{}")
	}

	p := &pb.Iec104Point{
		AsduTyp:     typeNameFromID(typeId),
		AsduHigh:    int32(ca >> 8),
		AsduLow:     int32(ca & 0xFF),
		IoaHigh:     int32((ioa >> 16) & 0xFF),
		IoaMiddle:   int32((ioa >> 8) & 0xFF),
		IoaLow:      int32(ioa & 0xFF),
		PayloadJson: string(payloadJSON),
		UpdatedAt:   time.Now().UnixMilli(),
	}
	if known := s.reg.lookup(ca, ioa); known != nil {
		p.Id = known.GetId()
		p.Type = known.GetType()
		p.Designation = known.GetDesignation()
		p.ChannelId = known.GetChannelId()
		p.ChannelName = known.GetChannelName()
		p.Typ = known.GetTyp()
		p.CreatedAt = known.GetCreatedAt()
	}
	s.hub.publish(p)
}

// commonAddrFromPoint reassembles the 2-byte Common Address split across
// Iec104Point.asdu_high/asdu_low (big-endian: high byte first).
func commonAddrFromPoint(p *pb.Iec104Point) asdu.CommonAddr {
	return asdu.CommonAddr(uint16(p.GetAsduHigh())<<8 | uint16(p.GetAsduLow())&0xFF)
}

// infoObjAddrFromPoint reassembles the 3-byte Information Object Address split
// across Iec104Point.ioa_high/ioa_middle/ioa_low (big-endian: high byte first).
func infoObjAddrFromPoint(p *pb.Iec104Point) asdu.InfoObjAddr {
	return asdu.InfoObjAddr(uint32(p.GetIoaHigh())<<16 | uint32(p.GetIoaMiddle())<<8 | uint32(p.GetIoaLow())&0xFF)
}

// startGRPCServer starts the Iec104Bridge gRPC server on a Unix Domain Socket
// in the background and returns the *grpc.Server so callers can GracefulStop it.
func startGRPCServer(socketPath string, srv *bridgeServer) (*grpc.Server, error) {
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("removing stale socket %s: %w", socketPath, err)
	}
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("grpc listen on unix://%s: %w", socketPath, err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterIec104BridgeServer(grpcServer, srv)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Printf("[grpc] server stopped: %v", err)
		}
	}()
	log.Printf("[grpc] Iec104Bridge listening on unix://%s", socketPath)
	return grpcServer, nil
}
