package main

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
)

// msl5LikeServer mimics MSL5's server behaviour: accepts the seven command
// types with COT 6 and mirrors them as ActCon, ignores other causes, rejects
// everything else with an error (the library then answers COT 44).
type msl5LikeServer struct {
	mu      sync.Mutex
	cmds    []*asdu.ASDU
	session asdu.Connect
}

func (s *msl5LikeServer) ASDUHandler(c asdu.Connect, p *asdu.ASDU) error {
	if !isCommandType(p.Type) {
		return asdu.ErrTypeIDNotMatch
	}
	if p.Identifier.Coa.Cause != asdu.Activation {
		return nil
	}
	s.mu.Lock()
	s.cmds = append(s.cmds, p)
	s.mu.Unlock()
	return p.SendReplyMirror(c, asdu.ActivationCon)
}
func (s *msl5LikeServer) InterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfInterrogation) error {
	return nil
}
func (s *msl5LikeServer) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierCountCall) error {
	return nil
}
func (s *msl5LikeServer) ReadHandler(asdu.Connect, *asdu.ASDU, asdu.InfoObjAddr) error { return nil }
func (s *msl5LikeServer) ClockSyncHandler(asdu.Connect, *asdu.ASDU, time.Time) error   { return nil }
func (s *msl5LikeServer) ResetProcessHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfResetProcessCmd) error {
	return nil
}
func (s *msl5LikeServer) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU, uint16) error { return nil }

func (s *msl5LikeServer) received(typ asdu.TypeID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.cmds {
		if a.Type == typ {
			n++
		}
	}
	return n
}

func (s *msl5LikeServer) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cmds)
}

func (s *msl5LikeServer) conn() asdu.Connect {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}

func startMSL5LikeServer(t *testing.T, addr string, h *msl5LikeServer) *cs104.Server {
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
// server: STARTDT, monitoring frames into the image, one command per point and
// tick, no command loop after the ActCon mirror, and resume after the server
// restarts.
func TestLoopbackAgainstRealServer(t *testing.T) {
	addr := freeAddr(t)
	h := &msl5LikeServer{}
	srv := startMSL5LikeServer(t, addr, h)

	const interval = 300 * time.Millisecond
	cli, snd, st, err := newClient(addr, 1, interval, false, 100*time.Millisecond, logConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		snd.Stop()
		_ = cli.Close()
	}()

	points := len(defaultCommandPoints())
	waitFor(t, "first command round", func() bool { return h.total() >= points })
	if h.received(asdu.C_SC_NA_1) < 1 || h.received(asdu.C_BO_NA_1) < 1 {
		t.Error("expected commands of every configured type")
	}

	waitFor(t, "session", func() bool { return h.conn() != nil })
	for _, build := range monitoringFrames(1) {
		if err := build(h.conn()); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "monitoring image", func() bool { return len(st.Snapshot()) == 6 })
	waitFor(t, "confirmations recorded", func() bool {
		_, ok := st.Confirmation(pointKey{1, 100})
		return ok
	})

	// Only the periodic cycle may produce commands: every round is complete,
	// so ActCon mirrors never trigger extra commands.
	time.Sleep(3 * interval)
	if n := h.total(); n%points != 0 {
		t.Errorf("server received %d commands, want a multiple of %d (command loop?)", n, points)
	}

	_ = srv.Close()
	h2 := &msl5LikeServer{}
	srv2 := startMSL5LikeServer(t, addr, h2)
	defer srv2.Close()
	waitFor(t, "commands after reconnect", func() bool { return h2.total() >= points })
}

// TestNoCommandLoop: with a long period only the initial round is sent, even
// though the server mirrors every command as ActCon.
func TestNoCommandLoop(t *testing.T) {
	addr := freeAddr(t)
	h := &msl5LikeServer{}
	srv := startMSL5LikeServer(t, addr, h)
	defer srv.Close()

	cli, snd, _, err := newClient(addr, 1, time.Hour, false, 100*time.Millisecond, logConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		snd.Stop()
		_ = cli.Close()
	}()

	points := len(defaultCommandPoints())
	waitFor(t, "initial round", func() bool { return h.total() >= points })
	time.Sleep(time.Second)
	if got := h.total(); got != points {
		t.Fatalf("server received %d commands, want exactly %d", got, points)
	}
}
