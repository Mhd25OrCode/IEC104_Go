package main

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
)

// captureConn is the asdu.Connect implementation used by the tests. It records
// every frame the station sends and never touches a real socket.
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
		t.Fatalf("frame %d missing, station sent %d", i, len(c.frames))
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
		t.Fatalf("station sent %d frames, want %d", got, n)
	}
}

// request builds a master-to-outstation frame and decodes it again, so handlers
// receive exactly what MSL5 would send.
func request(t *testing.T, build func(c asdu.Connect) error) *asdu.ASDU {
	t.Helper()
	master := newCaptureConn(t)
	if err := build(master); err != nil {
		t.Fatalf("building request: %v", err)
	}
	return master.frame(t, 0)
}

func newTestStation() *station {
	return &station{
		spVal: true,
		dpVal: asdu.DPIDeterminedOn,
		ana:   100,
	}
}

func commandIOA(sf *asdu.ASDU) asdu.InfoObjAddr {
	switch sf.Type {
	case asdu.C_SC_NA_1:
		return sf.GetSingleCmd().Ioa
	case asdu.C_DC_NA_1:
		return sf.GetDoubleCmd().Ioa
	case asdu.C_SE_NA_1:
		return sf.GetSetpointNormalCmd().Ioa
	case asdu.C_SE_NB_1:
		return sf.GetSetpointCmdScaled().Ioa
	case asdu.C_SE_NC_1:
		return sf.GetSetpointFloatCmd().Ioa
	case asdu.C_BO_NA_1:
		return sf.GetBitsString32Cmd().Ioa
	}
	return asdu.InfoObjAddrIrrelevant
}

// TestCommandsApplyToProcessImage checks that each command type used by MSL5 is
// stored and answered with exactly ActivationCon then ActivationTerm on the
// request CA.
func TestCommandsApplyToProcessImage(t *testing.T) {
	const ca asdu.CommonAddr = 7

	cases := []struct {
		name  string
		build func(c asdu.Connect) error
		typ   asdu.TypeID
		ioa   asdu.InfoObjAddr
		check func(t *testing.T, st *station)
	}{
		{
			name: "single C_SC_NA_1",
			build: func(c asdu.Connect) error {
				return asdu.SingleCmd(c, asdu.C_SC_NA_1, coa(asdu.Activation), ca,
					asdu.SingleCommandInfo{Ioa: 100, Value: false})
			},
			typ: asdu.C_SC_NA_1,
			ioa: 100,
			check: func(t *testing.T, st *station) {
				if st.spVal {
					t.Fatal("spVal = true, want false")
				}
			},
		},
		{
			name: "double C_DC_NA_1",
			build: func(c asdu.Connect) error {
				return asdu.DoubleCmd(c, asdu.C_DC_NA_1, coa(asdu.Activation), ca,
					asdu.DoubleCommandInfo{Ioa: 200, Value: asdu.DoubleCommand(asdu.DPIDeterminedOff)})
			},
			typ: asdu.C_DC_NA_1,
			ioa: 200,
			check: func(t *testing.T, st *station) {
				if st.dpVal != asdu.DPIDeterminedOff {
					t.Fatalf("dpVal = %v, want %v", st.dpVal, asdu.DPIDeterminedOff)
				}
			},
		},
		{
			name: "setpoint normal C_SE_NA_1 keeps the raw int16 value",
			build: func(c asdu.Connect) error {
				return asdu.SetpointCmdNormal(c, asdu.C_SE_NA_1, coa(asdu.Activation), ca,
					asdu.SetpointCommandNormalInfo{Ioa: 301, Value: asdu.Normalize(42)})
			},
			typ: asdu.C_SE_NA_1,
			ioa: 301,
			check: func(t *testing.T, st *station) {
				if st.ana != 42 {
					t.Fatalf("ana = %v, want 42", st.ana)
				}
			},
		},
		{
			name: "setpoint scaled C_SE_NB_1",
			build: func(c asdu.Connect) error {
				return asdu.SetpointCmdScaled(c, asdu.C_SE_NB_1, coa(asdu.Activation), ca,
					asdu.SetpointCommandScaledInfo{Ioa: 301, Value: -7})
			},
			typ: asdu.C_SE_NB_1,
			ioa: 301,
			check: func(t *testing.T, st *station) {
				if st.ana != -7 {
					t.Fatalf("ana = %v, want -7", st.ana)
				}
			},
		},
		{
			name: "setpoint float C_SE_NC_1",
			build: func(c asdu.Connect) error {
				return asdu.SetpointCmdFloat(c, asdu.C_SE_NC_1, coa(asdu.Activation), ca,
					asdu.SetpointCommandFloatInfo{Ioa: 301, Value: 42.5})
			},
			typ: asdu.C_SE_NC_1,
			ioa: 301,
			check: func(t *testing.T, st *station) {
				if st.ana != 42.5 {
					t.Fatalf("ana = %v, want 42.5", st.ana)
				}
			},
		},
		{
			name: "bitstring C_BO_NA_1",
			build: func(c asdu.Connect) error {
				return asdu.BitsString32Cmd(c, asdu.C_BO_NA_1, coa(asdu.Activation), ca,
					asdu.BitsString32CommandInfo{Ioa: 400, Value: 0x0F0F0F0F})
			},
			typ: asdu.C_BO_NA_1,
			ioa: 400,
			check: func(t *testing.T, st *station) {
				if st.bits != 0x0F0F0F0F {
					t.Fatalf("bits = 0x%08X, want 0x0F0F0F0F", st.bits)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStation()
			kicks := 0
			st.onChange = func() { kicks++ }
			conn := newCaptureConn(t)
			if err := st.OnASDU(conn, request(t, tc.build)); err != nil {
				t.Fatalf("OnASDU: %v", err)
			}
			tc.check(t, st)
			if kicks != 1 {
				t.Errorf("report kicked %d times, want 1", kicks)
			}

			conn.wantFrames(t, 2)
			for i, want := range []asdu.Cause{asdu.ActivationCon, asdu.ActivationTerm} {
				sf := conn.frame(t, i)
				if sf.Type != tc.typ {
					t.Errorf("frame %d type = %v, want %v", i, sf.Type, tc.typ)
				}
				if got := sf.Identifier.Coa.Cause; got != want {
					t.Errorf("frame %d cause = %v, want %v", i, got, want)
				}
				if sf.CommonAddr != ca {
					t.Errorf("frame %d CA = %d, want %d", i, sf.CommonAddr, ca)
				}
				if got := commandIOA(sf); got != tc.ioa {
					t.Errorf("frame %d IOA = %d, want %d", i, got, tc.ioa)
				}
			}
		})
	}
}

// TestOutOfScopeRequestsAreNotAnswered feeds interrogation, counter
// interrogation, read and clock sync requests into the handler.
func TestOutOfScopeRequestsAreNotAnswered(t *testing.T) {
	const ca asdu.CommonAddr = 5
	h := &clientHandler{st: newTestStation()}
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

func newTestReporter(t *testing.T, conn asdu.Connect, st *station, cyclic bool) *reporter {
	t.Helper()
	return newReporter(st, conn, 3, 0, cyclic)
}

// TestReportAllFrames checks the six spontaneous frames in table order.
func TestReportAllFrames(t *testing.T) {
	st := newTestStation()
	conn := newCaptureConn(t)
	newTestReporter(t, conn, st, false).reportAll()

	conn.wantFrames(t, 6)
	wantTypes := []asdu.TypeID{asdu.M_SP_NA_1, asdu.M_DP_NA_1, asdu.M_ME_NC_1, asdu.M_ME_TF_1, asdu.M_ME_NB_1, asdu.M_IT_NA_1}
	wantIOAs := []asdu.InfoObjAddr{100, 200, 301, 302, 303, 400}
	for i := range wantTypes {
		sf := conn.frame(t, i)
		if sf.Type != wantTypes[i] {
			t.Errorf("frame %d type = %v, want %v", i, sf.Type, wantTypes[i])
		}
		if got := sf.Identifier.Coa.Cause; got != asdu.Spontaneous {
			t.Errorf("frame %d cause = %v, want Spontaneous", i, got)
		}
		if sf.CommonAddr != 3 {
			t.Errorf("frame %d CA = %d, want 3", i, sf.CommonAddr)
		}
		var ioa asdu.InfoObjAddr
		switch sf.Type {
		case asdu.M_SP_NA_1:
			ioa = sf.GetSinglePoint()[0].Ioa
		case asdu.M_DP_NA_1:
			ioa = sf.GetDoublePoint()[0].Ioa
		case asdu.M_ME_NC_1:
			ioa = sf.GetMeasuredValueFloat()[0].Ioa
		case asdu.M_ME_TF_1:
			got := sf.GetMeasuredValueFloat()[0]
			ioa = got.Ioa
			if got.Time.IsZero() {
				t.Error("M_ME_TF_1 has no time tag")
			}
		case asdu.M_ME_NB_1:
			ioa = sf.GetMeasuredValueScaled()[0].Ioa
		case asdu.M_IT_NA_1:
			ioa = sf.GetIntegratedTotals()[0].Ioa
		}
		if ioa != wantIOAs[i] {
			t.Errorf("frame %d IOA = %d, want %d", i, ioa, wantIOAs[i])
		}
	}
}

// TestReportUsesImageValues checks reported values follow the process image,
// the counter and the int16 clamp.
func TestReportUsesImageValues(t *testing.T) {
	st := newTestStation()
	st.spVal = false
	st.dpVal = asdu.DPIIndeterminateOrIntermediate // 3 must be reported as 0/1
	st.ana = 70000
	st.counter = 9
	conn := newCaptureConn(t)
	newTestReporter(t, conn, st, false).reportAll()

	if got := conn.frame(t, 0).GetSinglePoint()[0].Value; got {
		t.Error("single point = true, want false")
	}
	if got := conn.frame(t, 1).GetDoublePoint()[0].Value; got != asdu.DPIDeterminedOn {
		t.Errorf("double point = %v, want %v", got, asdu.DPIDeterminedOn)
	}
	if got := conn.frame(t, 2).GetMeasuredValueFloat()[0].Value; got != 70000 {
		t.Errorf("float = %v, want 70000", got)
	}
	if got := conn.frame(t, 4).GetMeasuredValueScaled()[0].Value; got != 32767 {
		t.Errorf("scaled = %v, want 32767 (clamped)", got)
	}
	if got := conn.frame(t, 5).GetIntegratedTotals()[0].Value.CounterReading; got != 9 {
		t.Errorf("counter = %v, want 9", got)
	}
}

func TestCyclicModeOnlyForMeasurements(t *testing.T) {
	conn := newCaptureConn(t)
	newTestReporter(t, conn, newTestStation(), true).reportAll()

	conn.wantFrames(t, 6)
	for i, want := range []asdu.Cause{
		asdu.Spontaneous, asdu.Spontaneous, asdu.Periodic, asdu.Spontaneous, asdu.Periodic, asdu.Spontaneous,
	} {
		if got := conn.frame(t, i).Identifier.Coa.Cause; got != want {
			t.Errorf("frame %d cause = %v, want %v", i, got, want)
		}
	}
}

func TestReporterDoesNotSendWhenStopped(t *testing.T) {
	st := newTestStation()
	conn := newCaptureConn(t)
	rep := newTestReporter(t, conn, st, false)
	rep.Kick()
	rep.Stop()
	time.Sleep(2 * kickDebounce)
	conn.wantFrames(t, 0)
}

func TestReporterStartStopIdempotent(t *testing.T) {
	conn := newCaptureConn(t)
	rep := newReporter(newTestStation(), conn, 1, 20*time.Millisecond, false)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rep.Start()
			rep.Stop()
			rep.Start()
		}()
	}
	wg.Wait()
	rep.Start()
	time.Sleep(100 * time.Millisecond)
	rep.Stop()
	rep.Stop()

	n := conn.count()
	if n == 0 || n%6 != 0 {
		t.Fatalf("sent %d frames, want a positive multiple of 6", n)
	}
	time.Sleep(60 * time.Millisecond)
	if got := conn.count(); got != n {
		t.Fatalf("frames still sent after Stop: %d -> %d", n, got)
	}
}

func TestReporterKickReportsChange(t *testing.T) {
	conn := newCaptureConn(t)
	rep := newReporter(newTestStation(), conn, 1, 0, false)
	rep.Start()
	defer rep.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for conn.count() < 6 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	conn.wantFrames(t, 6)
	rep.Kick()
	for conn.count() < 12 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	conn.wantFrames(t, 12)
}

func TestSendErrorsAreNotFatal(t *testing.T) {
	for _, sendErr := range []error{cs104.ErrNotActive, cs104.ErrBufferFulled, cs104.ErrUseClosedConnection, errors.New("boom")} {
		conn := newCaptureConn(t)
		conn.sendErr = sendErr
		rep := newTestReporter(t, conn, newTestStation(), false)
		rep.reportAll()

		st := newTestStation()
		pack := request(t, func(c asdu.Connect) error {
			return asdu.SingleCmd(c, asdu.C_SC_NA_1, coa(asdu.Activation), 1,
				asdu.SingleCommandInfo{Ioa: 100, Value: false})
		})
		if err := st.OnASDU(conn, pack); err != nil {
			t.Errorf("OnASDU with send error %v returned %v, want nil", sendErr, err)
		}
	}
}
