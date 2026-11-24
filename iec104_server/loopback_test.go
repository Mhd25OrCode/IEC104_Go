package main

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
)

// loopbackServer is a minimal MSL5 stand-in: it records ASDUs and exposes the
// active session so tests can send commands.
type loopbackServer struct {
	mu      sync.Mutex
	asdus   []*asdu.ASDU
	session asdu.Connect
}

func (s *loopbackServer) ASDUHandler(c asdu.Connect, p *asdu.ASDU) error {
	s.mu.Lock()
	s.asdus = append(s.asdus, p)
	s.mu.Unlock()
	return nil
}
func (s *loopbackServer) InterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfInterrogation) error {
	return nil
}
func (s *loopbackServer) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierCountCall) error {
	return nil
}
func (s *loopbackServer) ReadHandler(asdu.Connect, *asdu.ASDU, asdu.InfoObjAddr) error { return nil }
func (s *loopbackServer) ClockSyncHandler(asdu.Connect, *asdu.ASDU, time.Time) error   { return nil }
func (s *loopbackServer) ResetProcessHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfResetProcessCmd) error {
	return nil
}
func (s *loopbackServer) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU, uint16) error { return nil }

func (s *loopbackServer) count(typ asdu.TypeID, cause asdu.Cause) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.asdus {
		if a.Type == typ && a.Identifier.Coa.Cause == cause {
			n++
		}
	}
	return n
}

func (s *loopbackServer) conn() asdu.Connect {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}

func startLoopbackServer(t *testing.T, addr string, h *loopbackServer) *cs104.Server {
	t.Helper()
	srv := cs104.NewServer(h)
	srv.SetOnConnectionHandler(func(c asdu.Connect) {
		h.mu.Lock()
		h.session = c
		h.mu.Unlock()
	})
	go srv.ListenAndServer(addr)
	return srv
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// TestLoopbackAgainstRealServer runs the real client wiring against a cs104
// server: STARTDT, spontaneous image, command with ActCon/ActTerm, report of
// the change, and reconnect after the server restarts.
func TestLoopbackAgainstRealServer(t *testing.T) {
	addr := freeAddr(t)
	h := &loopbackServer{}
	srv := startLoopbackServer(t, addr, h)

	cli, rep, err := newClient(addr, 1, 0, false, 100*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		rep.Stop()
		_ = cli.Close()
	}()

	waitFor(t, "initial image", func() bool {
		return h.count(asdu.M_SP_NA_1, asdu.Spontaneous) >= 1 && h.count(asdu.M_IT_NA_1, asdu.Spontaneous) >= 1
	})
	if h.count(asdu.M_ME_NA_1, asdu.Spontaneous) != 0 {
		t.Error("client reported M_ME_NA_1")
	}

	waitFor(t, "session", func() bool { return h.conn() != nil })
	if err := asdu.SingleCmd(h.conn(), asdu.C_SC_NA_1, coa(asdu.Activation), 1,
		asdu.SingleCommandInfo{Ioa: 100, Value: false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ActCon and ActTerm", func() bool {
		return h.count(asdu.C_SC_NA_1, asdu.ActivationCon) == 1 && h.count(asdu.C_SC_NA_1, asdu.ActivationTerm) == 1
	})
	waitFor(t, "report after command", func() bool { return h.count(asdu.M_SP_NA_1, asdu.Spontaneous) >= 2 })

	_ = srv.Close()
	h2 := &loopbackServer{}
	srv2 := startLoopbackServer(t, addr, h2)
	defer srv2.Close()
	waitFor(t, "image after reconnect", func() bool { return h2.count(asdu.M_SP_NA_1, asdu.Spontaneous) >= 1 })
}
