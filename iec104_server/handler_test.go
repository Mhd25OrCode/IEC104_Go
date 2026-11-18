package main

import (
	"net"
	"testing"

	"github.com/wendy512/go-iecp5/asdu"
)

// captureConn is the asdu.Connect implementation used by the tests. It records
// every frame the station sends and never touches a real socket, so the
// assertions can inspect type, cause, CA, IOA and value on the wire.
type captureConn struct {
	params *asdu.Params
	frames [][]byte
	conn   net.Conn
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
	raw, err := u.MarshalBinary()
	if err != nil {
		return err
	}
	c.frames = append(c.frames, raw)
	return nil
}

func (c *captureConn) UnderlyingConn() net.Conn { return c.conn }

// frame decodes the i-th frame captured from the station.
func (c *captureConn) frame(t *testing.T, i int) *asdu.ASDU {
	t.Helper()
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
	if len(c.frames) != n {
		t.Fatalf("station sent %d frames, want %d", len(c.frames), n)
	}
}

// request builds a master-to-outstation frame with the library helper and
// decodes it again, so handlers receive exactly what a master would send.
func request(t *testing.T, build func(c asdu.Connect) error) *asdu.ASDU {
	t.Helper()
	master := newCaptureConn(t)
	if err := build(master); err != nil {
		t.Fatalf("building request: %v", err)
	}
	return master.frame(t, 0)
}

// newTestStation returns a station in its power-on state. The outstation has no
// timers or background reporting, so a test station is always quiet.
func newTestStation() *station {
	return &station{
		spVal: true,
		dpVal: asdu.DPIDeterminedOn,
		ana:   100,
	}
}

// commandIOA returns the IOA a command confirmation echoes.
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

// TestCommandsApplyToProcessImage checks that each command type used by the MSL5
// master is stored in the process image and confirmed with its own type, COT 7
// and the CA of the request (never a configured outstation CA).
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
					t.Fatal("spVal = true, want false (command not applied)")
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
					t.Fatalf("ana = %v, want 42 (raw int16, no 1/32768 scaling)", st.ana)
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
			conn := newCaptureConn(t)
			if err := st.OnASDU(conn, request(t, tc.build)); err != nil {
				t.Fatalf("OnASDU: %v", err)
			}
			tc.check(t, st)

			conn.wantFrames(t, 1)
			sf := conn.frame(t, 0)
			if sf.Type != tc.typ {
				t.Errorf("confirmation type = %v, want %v", sf.Type, tc.typ)
			}
			if got := sf.Identifier.Coa.Cause; got != asdu.ActivationCon {
				t.Errorf("confirmation cause = %v, want %v", got, asdu.ActivationCon)
			}
			if sf.CommonAddr != ca {
				t.Errorf("confirmation CA = %d, want %d", sf.CommonAddr, ca)
			}
			if got := commandIOA(sf); got != tc.ioa {
				t.Errorf("confirmation IOA = %d, want %d", got, tc.ioa)
			}
		})
	}
}

// TestReadAnswersWithNativeTypes checks that a read command is answered with the
// ASDU type of the addressed object, COT 5 and the CA of the request.
func TestReadAnswersWithNativeTypes(t *testing.T) {
	const ca asdu.CommonAddr = 9

	cases := []struct {
		name  string
		ioa   asdu.InfoObjAddr
		typ   asdu.TypeID
		check func(t *testing.T, sf *asdu.ASDU)
	}{
		{
			name: "IOA 100 single point",
			ioa:  100,
			typ:  asdu.M_SP_NA_1,
			check: func(t *testing.T, sf *asdu.ASDU) {
				got := sf.GetSinglePoint()
				if len(got) != 1 || got[0].Ioa != 100 || !got[0].Value {
					t.Fatalf("single point = %+v, want IOA 100 value true", got)
				}
			},
		},
		{
			name: "IOA 200 double point",
			ioa:  200,
			typ:  asdu.M_DP_NA_1,
			check: func(t *testing.T, sf *asdu.ASDU) {
				got := sf.GetDoublePoint()
				if len(got) != 1 || got[0].Ioa != 200 || got[0].Value != asdu.DPIDeterminedOn {
					t.Fatalf("double point = %+v, want IOA 200 value %v", got, asdu.DPIDeterminedOn)
				}
			},
		},
		{
			name: "IOA 300 normalized measurement",
			ioa:  300,
			typ:  asdu.M_ME_NA_1,
			check: func(t *testing.T, sf *asdu.ASDU) {
				got := sf.GetMeasuredValueNormal()
				if len(got) != 1 || got[0].Value != normalizedValue {
					t.Fatalf("normalized = %+v, want value %d", got, normalizedValue)
				}
			},
		},
		{
			name: "IOA 301 short float",
			ioa:  301,
			typ:  asdu.M_ME_NC_1,
			check: func(t *testing.T, sf *asdu.ASDU) {
				got := sf.GetMeasuredValueFloat()
				if len(got) != 1 || got[0].Value != 100 {
					t.Fatalf("short float = %+v, want value 100", got)
				}
			},
		},
		{
			name: "IOA 302 time-tagged short float",
			ioa:  302,
			typ:  asdu.M_ME_TF_1,
			check: func(t *testing.T, sf *asdu.ASDU) {
				got := sf.GetMeasuredValueFloat()
				if len(got) != 1 || got[0].Value != 100 || got[0].Time.IsZero() {
					t.Fatalf("time-tagged float = %+v, want value 100 with CP56Time2a", got)
				}
			},
		},
		{
			name: "IOA 400 integrated total",
			ioa:  400,
			typ:  asdu.M_IT_NA_1,
			check: func(t *testing.T, sf *asdu.ASDU) {
				got := sf.GetIntegratedTotals()
				if len(got) != 1 || got[0].Value.CounterReading != 0 || got[0].Value.SeqNumber != 1 {
					t.Fatalf("integrated total = %+v, want reading 0 seq 1", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStation()
			conn := newCaptureConn(t)
			pack := request(t, func(c asdu.Connect) error {
				return asdu.ReadCmd(c, coa(asdu.Request), ca, tc.ioa)
			})
			if err := st.OnRead(conn, pack, tc.ioa); err != nil {
				t.Fatalf("OnRead: %v", err)
			}
			conn.wantFrames(t, 1)
			sf := conn.frame(t, 0)
			if sf.Type != tc.typ {
				t.Errorf("read answer type = %v, want %v", sf.Type, tc.typ)
			}
			if got := sf.Identifier.Coa.Cause; got != asdu.Request {
				t.Errorf("read answer cause = %v, want %v", got, asdu.Request)
			}
			if sf.CommonAddr != ca {
				t.Errorf("read answer CA = %d, want %d", sf.CommonAddr, ca)
			}
			tc.check(t, sf)
		})
	}
}

// TestReadUnknownIOA checks that an unmapped IOA is rejected with C_RD_NA_1 and
// COT 47 (unknown information object address) instead of an invented value.
func TestReadUnknownIOA(t *testing.T) {
	const ca asdu.CommonAddr = 9
	const ioa asdu.InfoObjAddr = 9999

	st := newTestStation()
	conn := newCaptureConn(t)
	pack := request(t, func(c asdu.Connect) error {
		return asdu.ReadCmd(c, coa(asdu.Request), ca, ioa)
	})
	if err := st.OnRead(conn, pack, ioa); err != nil {
		t.Fatalf("OnRead: %v", err)
	}

	conn.wantFrames(t, 1)
	sf := conn.frame(t, 0)
	if sf.Type != asdu.C_RD_NA_1 {
		t.Errorf("rejection type = %v, want %v", sf.Type, asdu.C_RD_NA_1)
	}
	if got := sf.Identifier.Coa.Cause; got != asdu.UnknownIOA {
		t.Errorf("rejection cause = %v, want %v", got, asdu.UnknownIOA)
	}
	if sf.CommonAddr != ca {
		t.Errorf("rejection CA = %d, want %d", sf.CommonAddr, ca)
	}
	if got := sf.GetReadCmd(); got != ioa {
		t.Errorf("rejection IOA = %d, want %d", got, ioa)
	}
}

// TestReadBackAfterCommand follows the write-then-read verification pattern of
// docs/iec104_test_outstation_change_plan.md: the value stored by the command is
// what the following read reports (the process image is the only source of
// truth and nothing overwrites it).
func TestReadBackAfterCommand(t *testing.T) {
	const ca asdu.CommonAddr = 3
	st := newTestStation()
	conn := newCaptureConn(t)

	steps := []func() error{
		func() error {
			return st.OnASDU(conn, request(t, func(c asdu.Connect) error {
				return asdu.SingleCmd(c, asdu.C_SC_NA_1, coa(asdu.Activation), ca,
					asdu.SingleCommandInfo{Ioa: 100, Value: false})
			}))
		},
		func() error {
			return st.OnRead(conn, request(t, func(c asdu.Connect) error {
				return asdu.ReadCmd(c, coa(asdu.Request), ca, 100)
			}), 100)
		},
		func() error {
			return st.OnASDU(conn, request(t, func(c asdu.Connect) error {
				return asdu.SetpointCmdFloat(c, asdu.C_SE_NC_1, coa(asdu.Activation), ca,
					asdu.SetpointCommandFloatInfo{Ioa: 301, Value: 42.5})
			}))
		},
		func() error {
			return st.OnRead(conn, request(t, func(c asdu.Connect) error {
				return asdu.ReadCmd(c, coa(asdu.Request), ca, 301)
			}), 301)
		},
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}

	conn.wantFrames(t, 4)
	sp := conn.frame(t, 1).GetSinglePoint()
	if len(sp) != 1 || sp[0].Value {
		t.Fatalf("read back of IOA 100 = %+v, want value false", sp)
	}
	ana := conn.frame(t, 3).GetMeasuredValueFloat()
	if len(ana) != 1 || ana[0].Value != 42.5 {
		t.Fatalf("read back of IOA 301 = %+v, want value 42.5", ana)
	}
}

// TestInterrogationSequenceEchoesRequestCA checks the standard general
// interrogation snapshot: ActivationCon -> data (COT 20) -> ActivationTerm, all
// on the CA of the request instead of a configured CA. The MSL5 master does not
// send a GI; this path exists for standard masters only.
func TestInterrogationSequenceEchoesRequestCA(t *testing.T) {
	const ca asdu.CommonAddr = 5
	st := newTestStation()
	conn := newCaptureConn(t)

	pack := request(t, func(c asdu.Connect) error {
		return asdu.InterrogationCmd(c, coa(asdu.Activation), ca, asdu.QOIStation)
	})
	if err := st.OnInterrogation(conn, pack, asdu.QOIStation); err != nil {
		t.Fatalf("OnInterrogation: %v", err)
	}

	conn.wantFrames(t, 7)
	wantTypes := []asdu.TypeID{
		asdu.C_IC_NA_1,
		asdu.M_SP_NA_1,
		asdu.M_DP_NA_1,
		asdu.M_ME_NA_1,
		asdu.M_ME_NC_1,
		asdu.M_ME_TF_1,
		asdu.C_IC_NA_1,
	}
	wantCauses := []asdu.Cause{
		asdu.ActivationCon,
		asdu.InterrogatedByStation,
		asdu.InterrogatedByStation,
		asdu.InterrogatedByStation,
		asdu.InterrogatedByStation,
		asdu.InterrogatedByStation,
		asdu.ActivationTerm,
	}
	for i := range wantTypes {
		sf := conn.frame(t, i)
		if sf.Type != wantTypes[i] {
			t.Errorf("frame %d type = %v, want %v", i, sf.Type, wantTypes[i])
		}
		if got := sf.Identifier.Coa.Cause; got != wantCauses[i] {
			t.Errorf("frame %d cause = %v, want %v", i, got, wantCauses[i])
		}
		if sf.CommonAddr != ca {
			t.Errorf("frame %d CA = %d, want %d", i, sf.CommonAddr, ca)
		}
	}
}

// TestCounterInterrogationEchoesRequestCA checks the counter-interrogation
// standard-master path: C_CI ActivationCon, M_IT_NA_1 with COT 37, C_CI
// ActivationTerm, all on the CA of the request.
func TestCounterInterrogationEchoesRequestCA(t *testing.T) {
	const ca asdu.CommonAddr = 5
	st := newTestStation()
	conn := newCaptureConn(t)

	qcc := asdu.QualifierCountCall{Request: asdu.QCCTotal}
	pack := request(t, func(c asdu.Connect) error {
		return asdu.CounterInterrogationCmd(c, coa(asdu.Activation), ca, qcc)
	})
	_, gotQCC := pack.GetCounterInterrogationCmd()
	if err := st.OnCounterInterrogation(conn, pack, gotQCC); err != nil {
		t.Fatalf("OnCounterInterrogation: %v", err)
	}

	conn.wantFrames(t, 3)
	wantTypes := []asdu.TypeID{asdu.C_CI_NA_1, asdu.M_IT_NA_1, asdu.C_CI_NA_1}
	wantCauses := []asdu.Cause{asdu.ActivationCon, asdu.RequestByGeneralCounter, asdu.ActivationTerm}
	for i := range wantTypes {
		sf := conn.frame(t, i)
		if sf.Type != wantTypes[i] {
			t.Errorf("frame %d type = %v, want %v", i, sf.Type, wantTypes[i])
		}
		if got := sf.Identifier.Coa.Cause; got != wantCauses[i] {
			t.Errorf("frame %d cause = %v, want %v", i, got, wantCauses[i])
		}
		if sf.CommonAddr != ca {
			t.Errorf("frame %d CA = %d, want %d", i, sf.CommonAddr, ca)
		}
	}
}

// TestCommandsAndReadsWorkWithoutInterrogation pins the no-GI requirement: on a
// fresh connection that never sent a general interrogation, commands are applied
// and reads answer immediately. The exact frame counts prove that no periodic,
// spontaneous or other unsolicited frame is produced.
func TestCommandsAndReadsWorkWithoutInterrogation(t *testing.T) {
	const ca asdu.CommonAddr = 4
	st := newTestStation()
	conn := newCaptureConn(t)

	read := func(ioa asdu.InfoObjAddr) {
		t.Helper()
		if err := st.OnRead(conn, request(t, func(c asdu.Connect) error {
			return asdu.ReadCmd(c, coa(asdu.Request), ca, ioa)
		}), ioa); err != nil {
			t.Fatalf("read IOA %d: %v", ioa, err)
		}
	}

	// The process image is readable before any command and without a GI.
	read(100)
	conn.wantFrames(t, 1)
	if got := conn.frame(t, 0).GetSinglePoint(); len(got) != 1 || !got[0].Value {
		t.Fatalf("initial read of IOA 100 = %+v, want value true", got)
	}

	// A single command works immediately and is confirmed with COT 7 on the
	// request CA.
	if err := st.OnASDU(conn, request(t, func(c asdu.Connect) error {
		return asdu.SingleCmd(c, asdu.C_SC_NA_1, coa(asdu.Activation), ca,
			asdu.SingleCommandInfo{Ioa: 100, Value: false})
	})); err != nil {
		t.Fatalf("single command: %v", err)
	}
	conn.wantFrames(t, 2)
	confirm := conn.frame(t, 1)
	if confirm.Type != asdu.C_SC_NA_1 {
		t.Errorf("confirmation type = %v, want %v", confirm.Type, asdu.C_SC_NA_1)
	}
	if got := confirm.Identifier.Coa.Cause; got != asdu.ActivationCon {
		t.Errorf("confirmation cause = %v, want %v", got, asdu.ActivationCon)
	}
	if confirm.CommonAddr != ca {
		t.Errorf("confirmation CA = %d, want %d", confirm.CommonAddr, ca)
	}
	read(100)
	conn.wantFrames(t, 3)
	if got := conn.frame(t, 2).GetSinglePoint(); len(got) != 1 || got[0].Value {
		t.Fatalf("read back of IOA 100 = %+v, want value false", got)
	}

	// The float set-point works immediately too and is visible at IOA 301/302.
	if err := st.OnASDU(conn, request(t, func(c asdu.Connect) error {
		return asdu.SetpointCmdFloat(c, asdu.C_SE_NC_1, coa(asdu.Activation), ca,
			asdu.SetpointCommandFloatInfo{Ioa: 301, Value: 42.5})
	})); err != nil {
		t.Fatalf("setpoint float command: %v", err)
	}
	conn.wantFrames(t, 4)
	read(301)
	read(302)
	conn.wantFrames(t, 6)
	for i := 0; i < 2; i++ {
		got := conn.frame(t, 4+i).GetMeasuredValueFloat()
		if len(got) != 1 || got[0].Value != 42.5 {
			t.Fatalf("read back frame %d = %+v, want value 42.5", 4+i, got)
		}
	}
}
