package main

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
)

// captureConn is the asdu.Connect implementation used by the tests. It records
// every frame sent and never touches a real socket.
type captureConn struct {
	mu      sync.Mutex
	params  *asdu.Params
	frames  [][]byte
	conn    net.Conn
	sendErr error
}

func newCaptureConn(t *testing.T) *captureConn {
	t.Helper()
	local, remote := net.Pipe()
	t.Cleanup(func() {
		_ = local.Close()
		_ = remote.Close()
	})
	return &captureConn{params: asdu.ParamsWide, conn: local}
}

func (c *captureConn) Params() *asdu.Params { return c.params }

func (c *captureConn) Send(u *asdu.ASDU) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sendErr != nil {
		return c.sendErr
	}
	raw, err := u.MarshalBinary()
	if err != nil {
		return err
	}
	c.frames = append(c.frames, raw)
	return nil
}

func (c *captureConn) UnderlyingConn() net.Conn { return c.conn }

func (c *captureConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

func (c *captureConn) frame(t *testing.T, i int) *asdu.ASDU {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.frames) {
		t.Fatalf("frame %d missing, %d sent", i, len(c.frames))
	}
	sf := asdu.NewASDU(asdu.ParamsWide, asdu.Identifier{})
	if err := sf.UnmarshalBinary(c.frames[i]); err != nil {
		t.Fatalf("frame %d does not decode: %v", i, err)
	}
	return sf
}

func (c *captureConn) wantFrames(t *testing.T, n int) {
	t.Helper()
	if got := c.count(); got != n {
		t.Fatalf("sent %d frames, want %d", got, n)
	}
}

// request builds a frame as MSL5 would send it and decodes it again, so
// handlers receive exactly what arrives on the wire.
func request(t *testing.T, build func(c asdu.Connect) error) *asdu.ASDU {
	t.Helper()
	peer := newCaptureConn(t)
	if err := build(peer); err != nil {
		t.Fatalf("building request: %v", err)
	}
	return peer.frame(t, 0)
}

func wantImage(t *testing.T, st *station, k pointKey, typ asdu.TypeID, value any) monitoringValue {
	t.Helper()
	got, ok := st.Snapshot()[k]
	if !ok {
		t.Fatalf("image has no entry for %+v", k)
	}
	if got.Type != typ || got.Value != value {
		t.Fatalf("%+v = {%v %v}, want {%v %v}", k, got.Type, got.Value, typ, value)
	}
	return got
}

func monitoringFrames(ca asdu.CommonAddr) []func(c asdu.Connect) error {
	spont := coa(asdu.Spontaneous)
	return []func(c asdu.Connect) error{
		func(c asdu.Connect) error {
			return asdu.Single(c, false, spont, ca, asdu.SinglePointInfo{Ioa: 100, Value: true, Qds: asdu.QDSBlocked})
		},
		func(c asdu.Connect) error {
			return asdu.Double(c, false, spont, ca, asdu.DoublePointInfo{Ioa: 200, Value: asdu.DPIDeterminedOff})
		},
		func(c asdu.Connect) error {
			return asdu.MeasuredValueFloat(c, false, spont, ca, asdu.MeasuredValueFloatInfo{Ioa: 301, Value: 12.5})
		},
		func(c asdu.Connect) error {
			return asdu.MeasuredValueFloatCP56Time2a(c, spont, ca,
				asdu.MeasuredValueFloatInfo{Ioa: 302, Value: 7.25, Time: time.Now()})
		},
		func(c asdu.Connect) error {
			return asdu.MeasuredValueScaled(c, false, spont, ca, asdu.MeasuredValueScaledInfo{Ioa: 303, Value: -42})
		},
		func(c asdu.Connect) error {
			return asdu.IntegratedTotals(c, false, spont, ca,
				asdu.BinaryCounterReadingInfo{Ioa: 400, Value: asdu.BinaryCounterReading{CounterReading: 99}})
		},
	}
}

func feedMonitoring(t *testing.T, st *station, ca asdu.CommonAddr) {
	t.Helper()
	conn := newCaptureConn(t)
	for _, build := range monitoringFrames(ca) {
		if err := st.OnASDU(conn, request(t, build)); err != nil {
			t.Fatalf("OnASDU: %v", err)
		}
	}
	conn.wantFrames(t, 0)
}

func TestIncomingMonitoringUpdatesImage(t *testing.T) {
	st := newStation(false)
	feedMonitoring(t, st, 1)

	wantImage(t, st, pointKey{1, 100}, asdu.M_SP_NA_1, true)
	if got := st.Snapshot()[pointKey{1, 100}].Qds; got != asdu.QDSBlocked {
		t.Errorf("qds = %v, want blocked", got)
	}
	wantImage(t, st, pointKey{1, 200}, asdu.M_DP_NA_1, uint8(asdu.DPIDeterminedOff))
	wantImage(t, st, pointKey{1, 301}, asdu.M_ME_NC_1, float32(12.5))
	if tf := wantImage(t, st, pointKey{1, 302}, asdu.M_ME_TF_1, float32(7.25)); tf.Time.IsZero() {
		t.Error("M_ME_TF_1 lost its CP56Time2a")
	}
	wantImage(t, st, pointKey{1, 303}, asdu.M_ME_NB_1, int16(-42))
	wantImage(t, st, pointKey{1, 400}, asdu.M_IT_NA_1, int32(99))
}

func TestIncomingMonitoringUsesFrameCA(t *testing.T) {
	st := newStation(false)
	feedMonitoring(t, st, 7)
	wantImage(t, st, pointKey{7, 100}, asdu.M_SP_NA_1, true)
	if _, ok := st.Snapshot()[pointKey{1, 100}]; ok {
		t.Error("entry stored under CA 1")
	}
}

func TestIncomingMonitoringMultipleInfoObjects(t *testing.T) {
	st := newStation(false)
	pack := request(t, func(c asdu.Connect) error {
		return asdu.MeasuredValueFloat(c, true, coa(asdu.Spontaneous), 1,
			asdu.MeasuredValueFloatInfo{Ioa: 500, Value: 1},
			asdu.MeasuredValueFloatInfo{Ioa: 501, Value: 2},
			asdu.MeasuredValueFloatInfo{Ioa: 502, Value: 3})
	})
	if err := st.OnASDU(newCaptureConn(t), pack); err != nil {
		t.Fatal(err)
	}
	for i, ioa := range []asdu.InfoObjAddr{500, 501, 502} {
		wantImage(t, st, pointKey{1, ioa}, asdu.M_ME_NC_1, float32(i+1))
	}
}

// TestConfirmationIsNotExecuted feeds COT 7 and COT 10 command frames plus a
// COT 6 echo: nothing is sent and the monitoring image stays untouched.
func TestConfirmationIsNotExecuted(t *testing.T) {
	st := newStation(false)
	conn := newCaptureConn(t)
	for _, cause := range []asdu.Cause{asdu.ActivationCon, asdu.ActivationTerm, asdu.Activation, asdu.Deactivation} {
		// The library builders only emit COT 6, so build the frame by hand.
		pack := request(t, func(c asdu.Connect) error {
			u := newCommandASDU(c, asdu.C_SC_NA_1, cause, 1)
			if err := u.AppendInfoObjAddr(100); err != nil {
				return err
			}
			u.AppendBytes(0x01)
			return c.Send(u)
		})
		if err := st.OnASDU(conn, pack); err != nil {
			t.Fatalf("COT %v: %v", cause, err)
		}
	}
	conn.wantFrames(t, 0)
	if n := len(st.Snapshot()); n != 0 {
		t.Fatalf("image has %d entries, want 0", n)
	}
	got, ok := st.Confirmation(pointKey{1, 100})
	if !ok || got.Cause != asdu.ActivationTerm {
		t.Fatalf("confirmation = %+v, %v; want ActivationTerm recorded", got, ok)
	}
}

// TestRejectedTypeGetsNoReply: types outside the contract are never answered.
func TestRejectedTypeGetsNoReply(t *testing.T) {
	st := newStation(false)
	conn := newCaptureConn(t)
	pack := request(t, func(c asdu.Connect) error {
		return asdu.MeasuredValueNormal(c, false, coa(asdu.Spontaneous), 1, asdu.MeasuredValueNormalInfo{Ioa: 1})
	})
	if err := st.OnASDU(conn, pack); err != nil {
		t.Fatal(err)
	}
	conn.wantFrames(t, 0)
}

// captureStdout runs fn and returns what it printed to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = old
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestFrameLogGating(t *testing.T) {
	run := func(logFrames bool) (rx, tx string) {
		// Getters consume a frame, so decode fresh ones for every run.
		unhandled := request(t, func(c asdu.Connect) error {
			return asdu.MeasuredValueNormal(c, false, coa(asdu.Spontaneous), 1, asdu.MeasuredValueNormalInfo{Ioa: 1})
		})
		monitored := request(t, monitoringFrames(1)[0])
		st := newStation(logFrames)
		rx = captureStdout(t, func() {
			_ = st.OnASDU(newCaptureConn(t), unhandled)
			_ = st.OnASDU(newCaptureConn(t), monitored)
		})

		conn := newCaptureConn(t)
		snd := newSender(conn, 3, defaultCommandPoints(), 0, false, logFrames, nil)
		snd.Set(100, true)
		tx = captureStdout(t, snd.sendAll)
		return rx, tx
	}
	if rx, tx := run(false); rx != "" || tx != "" {
		t.Errorf("default verbosity printed rx=%q tx=%q", rx, tx)
	}

	rx, tx := run(true)
	for _, want := range []string{"RX unhandled ASDU", "RX TID<M_SP_NA_1> COT", "IOA=100"} {
		if !strings.Contains(rx, want) {
			t.Errorf("RX log missing %q: %q", want, rx)
		}
	}
	for _, want := range []string{"TX TID<C_SC_NA_1> COT<Activation>", "CA=3", "IOA=100 value=true"} {
		if !strings.Contains(tx, want) {
			t.Errorf("TX log missing %q: %q", want, tx)
		}
	}
}

// TestOutOfScopeRequestsAreNotAnswered feeds interrogation, counter
// interrogation, read and clock sync requests into the handler.
func TestOutOfScopeRequestsAreNotAnswered(t *testing.T) {
	const ca asdu.CommonAddr = 5
	h := &clientHandler{st: newStation(false)}
	conn := newCaptureConn(t)

	reqs := []struct {
		name   string
		build  func(c asdu.Connect) error
		handle func(*asdu.ASDU) error
	}{
		{"C_IC_NA_1", func(c asdu.Connect) error {
			return asdu.InterrogationCmd(c, coa(asdu.Activation), ca, asdu.QOIStation)
		}, func(p *asdu.ASDU) error { return h.InterrogationHandler(conn, p) }},
		{"C_CI_NA_1", func(c asdu.Connect) error {
			return asdu.CounterInterrogationCmd(c, coa(asdu.Activation), ca, asdu.QualifierCountCall{Request: asdu.QCCTotal})
		}, func(p *asdu.ASDU) error { return h.CounterInterrogationHandler(conn, p) }},
		{"C_RD_NA_1", func(c asdu.Connect) error {
			return asdu.ReadCmd(c, coa(asdu.Request), ca, 100)
		}, func(p *asdu.ASDU) error { return h.ReadHandler(conn, p) }},
		{"C_CS_NA_1", func(c asdu.Connect) error {
			return asdu.ClockSynchronizationCmd(c, coa(asdu.Activation), ca, time.Now())
		}, func(p *asdu.ASDU) error { return h.ClockSyncHandler(conn, p) }},
	}
	for _, r := range reqs {
		if err := r.handle(request(t, r.build)); err != nil {
			t.Errorf("%s: handler returned %v, want nil", r.name, err)
		}
	}
	conn.wantFrames(t, 0)
}

func newTestSender(conn asdu.Connect, interval time.Duration, onChange bool) *sender {
	return newSender(conn, 3, defaultCommandPoints(), interval, onChange, false, nil)
}

func commandInfo(t *testing.T, sf *asdu.ASDU) (asdu.InfoObjAddr, any) {
	t.Helper()
	switch sf.Type {
	case asdu.C_SC_NA_1:
		c := sf.GetSingleCmd()
		return c.Ioa, c.Value
	case asdu.C_DC_NA_1:
		c := sf.GetDoubleCmd()
		return c.Ioa, uint8(c.Value)
	case asdu.C_RC_NA_1:
		c := sf.GetStepCmd()
		return c.Ioa, uint8(c.Value)
	case asdu.C_SE_NA_1:
		c := sf.GetSetpointNormalCmd()
		return c.Ioa, int16(c.Value)
	case asdu.C_SE_NB_1:
		c := sf.GetSetpointCmdScaled()
		return c.Ioa, c.Value
	case asdu.C_SE_NC_1:
		c := sf.GetSetpointFloatCmd()
		return c.Ioa, c.Value
	case asdu.C_BO_NA_1:
		c := sf.GetBitsString32Cmd()
		return c.Ioa, c.Value
	}
	t.Fatalf("unexpected type %v", sf.Type)
	return 0, nil
}

func TestOutgoingCommandsUseActivation(t *testing.T) {
	conn := newCaptureConn(t)
	snd := newTestSender(conn, 0, false)
	snd.Set(100, true)
	snd.Set(200, uint8(2))
	snd.Set(250, uint8(3))
	snd.Set(301, int16(-5))
	snd.Set(303, int16(1234))
	snd.Set(302, float32(42.5))
	snd.Set(400, uint32(0x0F0F0F0F))
	snd.sendAll()

	want := []struct {
		typ   asdu.TypeID
		ioa   asdu.InfoObjAddr
		value any
	}{
		{asdu.C_SC_NA_1, 100, true},
		{asdu.C_DC_NA_1, 200, uint8(2)},
		{asdu.C_RC_NA_1, 250, uint8(3)},
		{asdu.C_SE_NA_1, 301, int16(-5)},
		{asdu.C_SE_NB_1, 303, int16(1234)},
		{asdu.C_SE_NC_1, 302, float32(42.5)},
		{asdu.C_BO_NA_1, 400, uint32(0x0F0F0F0F)},
	}
	conn.wantFrames(t, len(want))
	for i, w := range want {
		sf := conn.frame(t, i)
		if sf.Type != w.typ || sf.Identifier.Coa.Cause != asdu.Activation || sf.CommonAddr != 3 {
			t.Errorf("frame %d = %v COT=%v CA=%d", i, sf.Type, sf.Identifier.Coa.Cause, sf.CommonAddr)
		}
		if sf.Variable.Number != 1 || sf.Variable.IsSequence {
			t.Errorf("frame %d is not a single-object ASDU", i)
		}
		if ioa, v := commandInfo(t, sf); ioa != w.ioa || v != w.value {
			t.Errorf("frame %d = IOA %d value %v, want IOA %d value %v", i, ioa, v, w.ioa, w.value)
		}
	}
}

func TestDoubleCommandValueIsClamped(t *testing.T) {
	conn := newCaptureConn(t)
	snd := newTestSender(conn, 0, false)
	snd.Set(200, uint8(9))
	snd.sendAll()
	for i := 0; i < conn.count(); i++ {
		if sf := conn.frame(t, i); sf.Type == asdu.C_DC_NA_1 {
			if _, v := commandInfo(t, sf); v != uint8(3) {
				t.Fatalf("value = %v, want 3", v)
			}
			return
		}
	}
	t.Fatal("no C_DC_NA_1 sent")
}

func TestSendOnChange(t *testing.T) {
	conn := newCaptureConn(t)
	snd := newTestSender(conn, 0, true)
	snd.Start()
	defer snd.Stop()

	time.Sleep(3 * kickDebounce)
	conn.wantFrames(t, 0) // interval 0: nothing without a change

	snd.Set(100, true)
	n := len(defaultCommandPoints())
	waitFor(t, "send-on-change round", func() bool { return conn.count() >= n })
	time.Sleep(3 * kickDebounce)
	conn.wantFrames(t, n)
	found := false
	for i := 0; i < n; i++ {
		if sf := conn.frame(t, i); sf.Type == asdu.C_SC_NA_1 {
			_, v := commandInfo(t, sf)
			found = v == true
		}
	}
	if !found {
		t.Error("changed value not sent")
	}
}

func TestSenderIdempotent(t *testing.T) {
	conn := newCaptureConn(t)
	snd := newTestSender(conn, 20*time.Millisecond, true)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snd.Start()
			snd.Set(100, true)
			snd.Stop()
			snd.Start()
		}()
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)
	snd.Stop()
	snd.Stop()

	per := len(defaultCommandPoints())
	n := conn.count()
	if n == 0 || n%per != 0 {
		t.Fatalf("sent %d frames, want a positive multiple of %d", n, per)
	}
	time.Sleep(60 * time.Millisecond)
	if got := conn.count(); got != n {
		t.Fatalf("frames still sent after Stop: %d -> %d", n, got)
	}
}

func TestSendErrorsAreNotFatal(t *testing.T) {
	for _, sendErr := range []error{cs104.ErrNotActive, cs104.ErrBufferFulled, cs104.ErrUseClosedConnection, errors.New("boom")} {
		conn := newCaptureConn(t)
		conn.sendErr = sendErr
		snd := newTestSender(conn, 10*time.Millisecond, true)
		snd.Start()
		time.Sleep(50 * time.Millisecond)
		snd.Stop()
		conn.wantFrames(t, 0)
	}
}

// imageSender returns a sender whose command values are resolved from st's
// monitoring image, exactly as newClient wires it in production.
func imageSender(t *testing.T, st *station, onChange bool) (*captureConn, *sender) {
	t.Helper()
	conn := newCaptureConn(t)
	return conn, newSender(conn, 1, defaultCommandPoints(), 0, onChange, false, st)
}

// frameOfType returns the first captured frame of type typ.
func frameOfType(t *testing.T, conn *captureConn, typ asdu.TypeID) *asdu.ASDU {
	t.Helper()
	for i := 0; i < conn.count(); i++ {
		if sf := conn.frame(t, i); sf.Type == typ {
			return sf
		}
	}
	t.Fatalf("no %v frame sent", typ)
	return nil
}

// TestCommandValueComesFromImage: a command point carries the value MSL5 last
// reported for the same CA + IOA instead of the configured default.
func TestCommandValueComesFromImage(t *testing.T) {
	st := newStation(false)
	feedMonitoring(t, st, 1)
	conn, snd := imageSender(t, st, false)
	snd.sendAll()
	conn.wantFrames(t, len(defaultCommandPoints()))

	want := []struct {
		typ   asdu.TypeID
		ioa   asdu.InfoObjAddr
		value any
	}{
		{asdu.C_SC_NA_1, 100, true},
		{asdu.C_DC_NA_1, 200, uint8(asdu.DPIDeterminedOff)},
		{asdu.C_SE_NB_1, 303, int16(-42)},
		{asdu.C_SE_NC_1, 302, float32(7.25)},
	}
	for _, w := range want {
		ioa, v := commandInfo(t, frameOfType(t, conn, w.typ))
		if ioa != w.ioa || v != w.value {
			t.Errorf("%v = IOA %d value %v, want IOA %d value %v", w.typ, ioa, v, w.ioa, w.value)
		}
	}
}

// TestCommandValueTypeMismatchKeepsDefault: only a monitoring type that maps
// onto the command type is used. An M_ME_NC_1 at IOA 301 must not feed the
// int16 C_SE_NA_1, and a counter reading must not feed a C_BO_NA_1 bit string.
func TestCommandValueTypeMismatchKeepsDefault(t *testing.T) {
	st := newStation(false)
	feedMonitoring(t, st, 1)
	conn, snd := imageSender(t, st, false)
	snd.sendAll()

	if _, v := commandInfo(t, frameOfType(t, conn, asdu.C_SE_NA_1)); v != int16(0) {
		t.Errorf("C_SE_NA_1 value = %v, want configured 0", v)
	}
	if _, v := commandInfo(t, frameOfType(t, conn, asdu.C_BO_NA_1)); v != uint32(0) {
		t.Errorf("C_BO_NA_1 value = %v, want configured 0", v)
	}
	if _, v := commandInfo(t, frameOfType(t, conn, asdu.C_RC_NA_1)); v != uint8(1) {
		t.Errorf("C_RC_NA_1 value = %v, want configured 1", v)
	}
}

// TestCommandValueIgnoresOtherCA: monitoring rows on another CA never feed the
// commands, which always carry -ca.
func TestCommandValueIgnoresOtherCA(t *testing.T) {
	st := newStation(false)
	feedMonitoring(t, st, 2)
	conn, snd := imageSender(t, st, false) // sender CA is 1
	snd.sendAll()

	if _, v := commandInfo(t, frameOfType(t, conn, asdu.C_SC_NA_1)); v != false {
		t.Errorf("C_SC_NA_1 value = %v, want configured false", v)
	}
}

// TestImageUpdateDoesNotSend: a monitoring frame only updates the image. Even
// with send-on-change nothing is sent, so MSL5's own M_* frames can never
// re-arm the sender (no command loop).
func TestImageUpdateDoesNotSend(t *testing.T) {
	st := newStation(false)
	conn, snd := imageSender(t, st, true)
	snd.Start()
	defer snd.Stop()

	feedMonitoring(t, st, 1)
	time.Sleep(3 * kickDebounce)
	conn.wantFrames(t, 0)
}
