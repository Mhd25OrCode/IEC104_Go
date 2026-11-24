package main

import (
	"fmt"
	"sync"

	"github.com/wendy512/go-iecp5/asdu"
)

// station is the process image of the dialling outstation.
//
// The image is written by the commands handled in OnASDU and reported to the
// MSL5 server by the reporter (spontaneous frames). General/counter
// interrogation and read commands are out of scope and never answered.
type station struct {
	mu      sync.Mutex
	spVal   bool
	dpVal   asdu.DoublePoint
	ana     float32
	counter int32
	bits    uint32 // last C_BO_NA_1 / C_BO_TA_1 bitstring command applied to the process image

	// onChange is called (outside mu) after a command changed the image.
	onChange func()
}

// replyOrLog reports a confirmation send failure without propagating it to the
// library dispatcher.
//
// cs104 would answer a non-nil handler error with a mirror of the request built
// from an ASDU whose info-object buffer was already consumed by Get*Cmd(), so
// it would arrive truncated (see iec_60870_5_104_bug_report.md §1-§4). Logging
// the failure here keeps the wire clean.
func replyOrLog(err error) {
	if err != nil {
		logSendError("confirmation", err)
	}
}

// confirmTwice sends ActivationCon followed by ActivationTerm using send.
func confirmTwice(send func(cause asdu.Cause) error) error {
	replyOrLog(send(asdu.ActivationCon))
	replyOrLog(send(asdu.ActivationTerm))
	return nil
}

// OnASDU handles control commands routed through ASDUHandler: the value is
// applied to the process image, then a fresh ActivationCon and ActivationTerm
// are sent (never a mirror of the consumed request). Both carry the Common
// Address of the request.
func (st *station) OnASDU(conn asdu.Connect, pack *asdu.ASDU) error {
	ca := pack.CommonAddr
	if pack.Identifier.Coa.Cause == asdu.Deactivation {
		fmt.Printf("[client] ignoring deactivation: type=%v CA=%d\n", pack.Type, ca)
		return nil
	}
	switch pack.Type {
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		sc := pack.GetSingleCmd()
		fmt.Printf("[client] single command: IOA=%d value=%v COT=%v CA=%d\n", sc.Ioa, sc.Value, pack.Identifier.Coa, ca)
		st.applySingle(sc.Value)
		return confirmTwice(func(c asdu.Cause) error { return confirmSingleCmd(conn, pack.Type, c, ca, sc) })
	case asdu.C_DC_NA_1, asdu.C_DC_TA_1:
		dc := pack.GetDoubleCmd()
		fmt.Printf("[client] double command: IOA=%d value=%d COT=%v CA=%d\n", dc.Ioa, dc.Value, pack.Identifier.Coa, ca)
		st.applyDouble(uint8(dc.Value))
		return confirmTwice(func(c asdu.Cause) error { return confirmDoubleCmd(conn, pack.Type, c, ca, dc) })
	case asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		bc := pack.GetBitsString32Cmd()
		fmt.Printf("[client] bitstring32 command: IOA=%d value=0x%08X COT=%v CA=%d\n", bc.Ioa, bc.Value, pack.Identifier.Coa, ca)
		st.applyBits(bc.Value)
		return confirmTwice(func(c asdu.Cause) error { return confirmBitsString32Cmd(conn, pack.Type, c, ca, bc) })
	case asdu.C_SE_NA_1, asdu.C_SE_TA_1:
		sp := pack.GetSetpointNormalCmd()
		fmt.Printf("[client] setpoint normal command: IOA=%d value=%v COT=%v CA=%d\n", sp.Ioa, sp.Value, pack.Identifier.Coa, ca)
		st.applyAnalog(float32(sp.Value))
		return confirmTwice(func(c asdu.Cause) error { return confirmSetpointNormal(conn, pack.Type, c, ca, sp) })
	case asdu.C_SE_NB_1, asdu.C_SE_TB_1:
		sp := pack.GetSetpointCmdScaled()
		fmt.Printf("[client] setpoint scaled command: IOA=%d value=%v COT=%v CA=%d\n", sp.Ioa, sp.Value, pack.Identifier.Coa, ca)
		st.applyAnalog(float32(sp.Value))
		return confirmTwice(func(c asdu.Cause) error { return confirmSetpointScaled(conn, pack.Type, c, ca, sp) })
	case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		sp := pack.GetSetpointFloatCmd()
		fmt.Printf("[client] setpoint float command: IOA=%d value=%v COT=%v CA=%d\n", sp.Ioa, sp.Value, pack.Identifier.Coa, ca)
		st.applyAnalog(sp.Value)
		return confirmTwice(func(c asdu.Cause) error { return confirmSetpointFloat(conn, pack.Type, c, ca, sp) })
	default:
		fmt.Printf("[client] unhandled ASDU: type=%v COT=%v CA=%d\n", pack.Type, pack.Identifier.Coa, ca)
		return nil
	}
}

// applySingle stores a single command in the process image (reported at IOA 100).
func (st *station) applySingle(v bool) {
	st.mu.Lock()
	st.spVal = v
	st.mu.Unlock()
	st.changed()
}

// applyDouble stores a double command (DPD bits only) in the process image.
func (st *station) applyDouble(v uint8) {
	st.mu.Lock()
	st.dpVal = asdu.DoublePoint(v & 0x03)
	st.mu.Unlock()
	st.changed()
}

// applyAnalog stores a set-point command in the float process image reported at
// IOA 301/302/303.
//
// C_SE_NA_1 and C_SE_NB_1 are both taken as the raw int16 value: MSL5 sends
// engineering values for the two int16 command forms, so re-scaling the
// normalized form by 1/32768 would be wrong.
func (st *station) applyAnalog(v float32) {
	st.mu.Lock()
	st.ana = v
	st.mu.Unlock()
	st.changed()
}

// applyBits stores a 32-bit bitstring command. It is kept in its own image
// field and is not reported as a monitored point.
func (st *station) applyBits(v uint32) {
	st.mu.Lock()
	st.bits = v
	st.mu.Unlock()
	st.changed()
}

// changed logs the process image and triggers a report of the new state.
func (st *station) changed() {
	st.mu.Lock()
	sp, dp, ana, bits := st.spVal, st.dpVal, st.ana, st.bits
	st.mu.Unlock()
	fmt.Printf("[client] applied: spVal=%v dpVal=%v ana=%v bits=0x%08X\n", sp, dp, ana, bits)
	if st.onChange != nil {
		st.onChange()
	}
}
