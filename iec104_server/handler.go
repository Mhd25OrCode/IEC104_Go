package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

// station implements server.CommandHandler with a small process image.
//
// The process image is the single source of truth: it is written only by the
// commands handled in OnASDU and read back on request (C_RD_NA_1 and, for
// standard masters, general/counter interrogation). The outstation never sends
// periodic or spontaneous data.
type station struct {
	mu      sync.Mutex
	spVal   bool
	dpVal   asdu.DoublePoint
	ana     float32
	counter int32
	bits    uint32 // last C_BO_NA_1 / C_BO_TA_1 bitstring command applied to the process image
}

// normalizedValue is the fixed process value reported for the normalized
// measurement at IOA 300 (10000/32768 ≈ 0.305 in per-unit notation). Commands
// do not change it: C_SE_* writes the short-float analog reported at IOA 301/302.
const normalizedValue asdu.Normalize = 10000

// sendAllData reports the whole process image with COT=20 (station
// interrogation response): single point, double point, normalized measurement,
// short-float measurement (M_ME_NC_1) and the time-tagged short-float
// measurement (M_ME_TF_1, IOA 302).
//
// This is a one-shot answer to a general interrogation, not a reporting loop.
// The MSL5 master reads single points instead (C_RD_NA_1), so it never triggers
// this; it exists so standard masters remain supported.
func (st *station) sendAllData(conn asdu.Connect, ca asdu.CommonAddr) error {
	st.mu.Lock()
	sp, dp, ana := st.spVal, st.dpVal, st.ana
	st.mu.Unlock()

	if err := asdu.Single(conn, false, coa(asdu.InterrogatedByStation), ca,
		asdu.SinglePointInfo{Ioa: 100, Value: sp, Qds: asdu.QDSGood}); err != nil {
		return err
	}
	if err := asdu.Double(conn, false, coa(asdu.InterrogatedByStation), ca,
		asdu.DoublePointInfo{Ioa: 200, Value: dp, Qds: asdu.QDSGood}); err != nil {
		return err
	}
	if err := asdu.MeasuredValueNormal(conn, false, coa(asdu.InterrogatedByStation), ca,
		asdu.MeasuredValueNormalInfo{Ioa: 300, Value: normalizedValue, Qds: asdu.QDSGood}); err != nil {
		return err
	}
	if err := asdu.MeasuredValueFloat(conn, false, coa(asdu.InterrogatedByStation), ca,
		asdu.MeasuredValueFloatInfo{Ioa: 301, Value: ana, Qds: asdu.QDSGood}); err != nil {
		return err
	}
	// The same analog reported with a CP56Time2a tag (M_ME_TF_1). Hand-built,
	// because asdu.MeasuredValueFloatCP56Time2a rejects COT 20.
	return sendMeasuredFloatTime(conn, asdu.InterrogatedByStation, ca,
		asdu.MeasuredValueFloatInfo{Ioa: 302, Value: ana, Qds: asdu.QDSGood, Time: time.Now()})
}

// sendCounters reports integrated totals with COT=37 (response to a general
// counter interrogation).
//
// The MSL5 master has no counter objects and never sends C_CI_NA_1; this answer
// keeps standard masters working. The counter image is never modified by
// commands, so it reports 0 unless the code is changed.
func (st *station) sendCounters(conn asdu.Connect, ca asdu.CommonAddr) error {
	st.mu.Lock()
	c := st.counter
	st.mu.Unlock()
	return asdu.IntegratedTotals(conn, false, coa(asdu.RequestByGeneralCounter), ca,
		asdu.BinaryCounterReadingInfo{
			Ioa:   400,
			Value: asdu.BinaryCounterReading{CounterReading: c, SeqNumber: 1},
		})
}

// OnInterrogation answers a general interrogation with the standard sequence
// ActivationCon -> interrogated data -> ActivationTerm.
//
// It is a one-shot snapshot of the process image on the request's Common
// Address. It neither starts nor depends on background reporting: commands and
// reads work without any interrogation. The MSL5 master never sends a general
// interrogation, so this path only serves standard masters.
func (st *station) OnInterrogation(conn asdu.Connect, pack *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	ca := pack.CommonAddr
	fmt.Printf("[server] general interrogation: COT=%v QOI=%d CA=%d\n", pack.Identifier.Coa.Cause, qoi, ca)
	if pack.Identifier.Coa.Cause == asdu.Deactivation {
		return confirmInterrogation(conn, asdu.DeactivationCon, ca, qoi)
	}
	var firstErr error
	if err := confirmInterrogation(conn, asdu.ActivationCon, ca, qoi); err != nil {
		firstErr = err
	}
	if err := st.sendAllData(conn, ca); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := confirmInterrogation(conn, asdu.ActivationTerm, ca, qoi); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// OnCounterInterrogation responds with ActivationCon, the counters and
// ActivationTerm, all carrying the Common Address of the request.
func (st *station) OnCounterInterrogation(conn asdu.Connect, pack *asdu.ASDU, qcc asdu.QualifierCountCall) error {
	ca := pack.CommonAddr
	fmt.Printf("[server] counter interrogation: COT=%v QCC=0x%02X CA=%d\n", pack.Identifier.Coa.Cause, qcc.Value(), ca)
	if err := confirmCounterInterrogation(conn, asdu.ActivationCon, ca, qcc); err != nil {
		return err
	}
	if err := st.sendCounters(conn, ca); err != nil {
		return err
	}
	return confirmCounterInterrogation(conn, asdu.ActivationTerm, ca, qcc)
}

// OnRead answers a read command (C_RD_NA_1) with the ASDU type that actually
// represents the addressed object, always with COT=5 and the Common Address of
// the request.
//
// The MSL5 master stores the answer per (CA, IOA) and expects the native type:
// M_SP_NA_1 for IOA 100, M_ME_NC_1/M_ME_TF_1 for the analog, M_IT_NA_1 for the
// counter. An unmapped IOA is answered with a C_RD_NA_1 carrying COT 47
// (unknown information object address) so the master can show the rejection.
func (st *station) OnRead(conn asdu.Connect, pack *asdu.ASDU, addr asdu.InfoObjAddr) error {
	ca := pack.CommonAddr
	fmt.Printf("[server] read command: IOA=%d CA=%d\n", addr, ca)

	st.mu.Lock()
	sp, dp, ana, c := st.spVal, st.dpVal, st.ana, st.counter
	st.mu.Unlock()

	switch addr {
	case 100:
		return asdu.Single(conn, false, coa(asdu.Request), ca,
			asdu.SinglePointInfo{Ioa: addr, Value: sp, Qds: asdu.QDSGood})
	case 200:
		return asdu.Double(conn, false, coa(asdu.Request), ca,
			asdu.DoublePointInfo{Ioa: addr, Value: dp, Qds: asdu.QDSGood})
	case 300:
		return asdu.MeasuredValueNormal(conn, false, coa(asdu.Request), ca,
			asdu.MeasuredValueNormalInfo{Ioa: addr, Value: normalizedValue, Qds: asdu.QDSGood})
	case 301:
		return asdu.MeasuredValueFloat(conn, false, coa(asdu.Request), ca,
			asdu.MeasuredValueFloatInfo{Ioa: addr, Value: ana, Qds: asdu.QDSGood})
	case 302:
		return sendMeasuredFloatTime(conn, asdu.Request, ca,
			asdu.MeasuredValueFloatInfo{Ioa: addr, Value: ana, Qds: asdu.QDSGood, Time: time.Now()})
	case 400:
		return sendCounterRequest(conn, asdu.Request, ca, addr,
			asdu.BinaryCounterReading{CounterReading: c, SeqNumber: 1})
	default:
		fmt.Printf("[server] read command: IOA=%d is not a monitored object\n", addr)
		return confirmReadError(conn, ca, asdu.UnknownIOA, addr)
	}
}

// OnClockSync acknowledges a clock synchronization command.
func (st *station) OnClockSync(conn asdu.Connect, pack *asdu.ASDU, tm time.Time) error {
	fmt.Printf("[server] clock sync request: %v CA=%d\n", tm.UTC(), pack.CommonAddr)
	return confirmClockSync(conn, pack.CommonAddr, tm)
}

// OnResetProcess acknowledges a reset-process command.
func (st *station) OnResetProcess(conn asdu.Connect, pack *asdu.ASDU, qrp asdu.QualifierOfResetProcessCmd) error {
	fmt.Printf("[server] reset process command: QRP=0x%02X CA=%d\n", qrp, pack.CommonAddr)
	return confirmResetProcess(conn, pack.CommonAddr, qrp)
}

// OnDelayAcquisition acknowledges a delay-acquisition command.
func (st *station) OnDelayAcquisition(conn asdu.Connect, pack *asdu.ASDU, msec uint16) error {
	fmt.Printf("[server] delay acquisition command: %d ms CA=%d\n", msec, pack.CommonAddr)
	return confirmDelayAcquire(conn, pack.CommonAddr, msec)
}

// OnTestCommand acknowledges a test command (C_TS_NA_1).
func (st *station) OnTestCommand(conn asdu.Connect, pack *asdu.ASDU) error {
	fmt.Printf("[server] test command (C_TS_NA_1) CA=%d\n", pack.CommonAddr)
	return confirmTestCommand(conn, pack.CommonAddr, time.Now())
}

// replyOrLog reports a confirmation send failure without propagating it to the
// library dispatcher.
//
// cs104's server_session.go answers a non-nil ASDUHandler error with
// asduPack.SendReplyMirror(sf, asdu.UnknownTypeID). That mirror would be built
// from an ASDU whose info-object buffer was already consumed by Get*Cmd(), so it
// would arrive empty/truncated and the peer would fail with
// "asdu UnmarshalBinary failed, EOF" (see iec_60870_5_104_bug_report.md §1-§4).
// Logging the failure here instead keeps the wire clean; the socket is broken
// anyway if a send failed.
func replyOrLog(conn asdu.Connect, err error) error {
	if err != nil {
		fmt.Printf("[server] confirmation send failed (%s): %v\n", conn.UnderlyingConn().RemoteAddr(), err)
	}
	return nil
}

// OnASDU handles control commands routed through ASDUHandler, applies them to
// the process image and echoes a fresh ActivationCon confirmation for each of
// them (never a mirror of the consumed request).
//
// Every confirmation carries the Common Address of the request, and the value is
// stored before the confirmation is sent: a C_RD_NA_1 issued right after a
// command therefore reads back what was written.
func (st *station) OnASDU(conn asdu.Connect, pack *asdu.ASDU) error {
	ca := pack.CommonAddr
	switch pack.Type {
	case asdu.C_TS_TA_1:
		fmt.Printf("[server] test command (C_TS_TA_1) CA=%d\n", ca)
		return replyOrLog(conn, confirmTestCommand(conn, ca, time.Now()))
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		sc := pack.GetSingleCmd()
		fmt.Printf("[server] single command: IOA=%d value=%v COT=%v CA=%d\n", sc.Ioa, sc.Value, pack.Identifier.Coa, ca)
		st.applySingle(sc.Value)
		return replyOrLog(conn, confirmSingleCmd(conn, pack.Type, ca, sc))
	case asdu.C_DC_NA_1, asdu.C_DC_TA_1:
		dc := pack.GetDoubleCmd()
		fmt.Printf("[server] double command: IOA=%d value=%d COT=%v CA=%d\n", dc.Ioa, dc.Value, pack.Identifier.Coa, ca)
		st.applyDouble(uint8(dc.Value))
		return replyOrLog(conn, confirmDoubleCmd(conn, pack.Type, ca, dc))
	case asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		bc := pack.GetBitsString32Cmd()
		fmt.Printf("[server] bitstring32 command: IOA=%d value=0x%08X COT=%v CA=%d\n", bc.Ioa, bc.Value, pack.Identifier.Coa, ca)
		st.applyBits(bc.Value)
		return replyOrLog(conn, confirmBitsString32Cmd(conn, pack.Type, ca, bc))
	case asdu.C_SE_NA_1, asdu.C_SE_TA_1:
		sp := pack.GetSetpointNormalCmd()
		fmt.Printf("[server] setpoint normal command: IOA=%d value=%v COT=%v CA=%d\n", sp.Ioa, sp.Value, pack.Identifier.Coa, ca)
		st.applyAnalog(float32(sp.Value))
		return replyOrLog(conn, confirmSetpointNormal(conn, pack.Type, ca, sp))
	case asdu.C_SE_NB_1, asdu.C_SE_TB_1:
		sp := pack.GetSetpointCmdScaled()
		fmt.Printf("[server] setpoint scaled command: IOA=%d value=%v COT=%v CA=%d\n", sp.Ioa, sp.Value, pack.Identifier.Coa, ca)
		st.applyAnalog(float32(sp.Value))
		return replyOrLog(conn, confirmSetpointScaled(conn, pack.Type, ca, sp))
	case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		sp := pack.GetSetpointFloatCmd()
		fmt.Printf("[server] setpoint float command: IOA=%d value=%v COT=%v CA=%d\n", sp.Ioa, sp.Value, pack.Identifier.Coa, ca)
		st.applyAnalog(sp.Value)
		return replyOrLog(conn, confirmSetpointFloat(conn, pack.Type, ca, sp))
	default:
		fmt.Printf("[server] unhandled ASDU: type=%v COT=%v CA=%d\n", pack.Type, pack.Identifier.Coa, ca)
		return nil
	}
}

// applySingle stores a single command in the process image. The IOA is not
// restricted: the value is what IOA 100 reports (GI snapshot and C_RD_NA_1).
func (st *station) applySingle(v bool) {
	st.mu.Lock()
	st.spVal = v
	st.mu.Unlock()
	st.logApplied()
}

// applyDouble stores a double command (DPD bits only) in the process image.
func (st *station) applyDouble(v uint8) {
	st.mu.Lock()
	st.dpVal = asdu.DoublePoint(v & 0x03)
	st.mu.Unlock()
	st.logApplied()
}

// applyAnalog stores a set-point command in the float process image reported at
// IOA 301/302.
//
// C_SE_NA_1 and C_SE_NB_1 are both taken as the raw int16 value: the MSL5 master
// sends engineering values for the two int16 command forms, so re-scaling the
// normalized form by 1/32768 would make a C_SE_NA_1 write read back 32768 times
// smaller than what the operator sent.
func (st *station) applyAnalog(v float32) {
	st.mu.Lock()
	st.ana = v
	st.mu.Unlock()
	st.logApplied()
}

// applyBits stores a 32-bit bitstring command (C_BO_NA_1/C_BO_TA_1). It is kept
// in its own image field and is not reported as a monitored point.
func (st *station) applyBits(v uint32) {
	st.mu.Lock()
	st.bits = v
	st.mu.Unlock()
	st.logApplied()
}

// logApplied prints the whole process image after a command was applied, so the
// outstation log can be compared with the confirmation payload shown by the
// master (verification step of docs/iec104_test_outstation_change_plan.md).
func (st *station) logApplied() {
	st.mu.Lock()
	sp, dp, ana, bits := st.spVal, st.dpVal, st.ana, st.bits
	st.mu.Unlock()
	fmt.Printf("[server] applied: spVal=%v dpVal=%v ana=%v bits=0x%08X\n", sp, dp, ana, bits)
}
