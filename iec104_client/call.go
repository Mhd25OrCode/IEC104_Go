package main

import (
	"fmt"
	"os"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/iec104/client"
)

// masterCall implements client.ASDUCall — everything the master receives.
type masterCall struct {
	// bridge forwards every inbound callback to the backend as an NDJSON event
	// frame; nil (-demo, tests) simply disables that forwarding.
	bridge *bridge
}

// logf writes diagnostics to stderr: stdout is reserved for protocol frames.
func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
}

// OnInterrogation logs every general-interrogation reply. The server sends
// three C_IC_NA_1 frames here: ActivationCon, then data (as separate M_*
// ASDUs handled by OnASDU), then ActivationTerm.
func (m *masterCall) OnInterrogation(packet *asdu.ASDU) error {
	addr, qoi := packet.GetInterrogationCmd()
	logf("[client] interrogation reply: IOA=%d QOI=%d COT=%v\n", addr, qoi, packet.Coa)
	m.bridge.publishEvent(packet.CommonAddr, addr, packet.Type, packet.Coa, qoi)
	return nil
}

// OnCounterInterrogation logs counter-interrogation replies.
func (m *masterCall) OnCounterInterrogation(packet *asdu.ASDU) error {
	addr, qcc := packet.GetCounterInterrogationCmd()
	logf("[client] counter interrogation reply: IOA=%d QCC=0x%02X COT=%v\n",
		addr, qcc.Value(), packet.Coa)
	m.bridge.publishEvent(packet.CommonAddr, addr, packet.Type, packet.Coa, qcc)
	return nil
}

// OnRead logs read replies (the server answers reads with data ASDUs, so this
// only triggers if the peer echoes the read command itself).
func (m *masterCall) OnRead(packet *asdu.ASDU) error {
	addr := packet.GetReadCmd()
	logf("[client] read reply: IOA=%d COT=%v\n", addr, packet.Coa)
	m.bridge.publishEvent(packet.CommonAddr, addr, packet.Type, packet.Coa, nil)
	return nil
}

// OnTestCommand logs untagged C_TS_NA_1 test-command replies.
func (m *masterCall) OnTestCommand(packet *asdu.ASDU) error {
	addr, fbp := packet.GetTestCommand()
	logf("[client] test command reply: IOA=%d valid-test-word=%v COT=%v\n", addr, fbp, packet.Coa)
	m.bridge.publishEvent(packet.CommonAddr, addr, packet.Type, packet.Coa, fbp)
	return nil
}

// OnClockSync logs clock-synchronization confirmations.
func (m *masterCall) OnClockSync(packet *asdu.ASDU) error {
	addr, tm := packet.GetClockSynchronizationCmd()
	logf("[client] clock sync reply: IOA=%d time=%s COT=%v\n",
		addr, tm.UTC().Format(time.RFC3339), packet.Coa)
	m.bridge.publishEvent(packet.CommonAddr, addr, packet.Type, packet.Coa, tm)
	return nil
}

// OnResetProcess logs reset-process confirmations.
func (m *masterCall) OnResetProcess(packet *asdu.ASDU) error {
	addr, qrp := packet.GetResetProcessCmd()
	logf("[client] reset process reply: IOA=%d QRP=0x%02X COT=%v\n", addr, qrp, packet.Coa)
	m.bridge.publishEvent(packet.CommonAddr, addr, packet.Type, packet.Coa, qrp)
	return nil
}

// OnDelayAcquisition logs delay-acquisition confirmations.
func (m *masterCall) OnDelayAcquisition(packet *asdu.ASDU) error {
	addr, msec := packet.GetDelayAcquireCommand()
	logf("[client] delay acquisition reply: IOA=%d msec=%d COT=%v\n", addr, msec, packet.Coa)
	m.bridge.publishEvent(packet.CommonAddr, addr, packet.Type, packet.Coa, msec)
	return nil
}

// OnASDU handles monitoring data and command confirmations that are not
// routed to a dedicated handler.
func (m *masterCall) OnASDU(packet *asdu.ASDU) error {
	cmd := m.onCommandReply(packet)
	if cmd {
		return nil
	}

	switch client.GetDataType(packet.Type) {
	case client.SinglePoint:
		for _, p := range packet.GetSinglePoint() {
			logf("[client] single point: IOA=%d value=%v QDS=%d COT=%v time=%s\n",
				p.Ioa, p.Value, p.Qds, packet.Coa, p.Time.Format(time.RFC3339Nano))
			m.bridge.publishEvent(packet.CommonAddr, p.Ioa, packet.Type, packet.Coa, p)
		}
	case client.DoublePoint:
		for _, p := range packet.GetDoublePoint() {
			logf("[client] double point: IOA=%d value=%d QDS=%d COT=%v time=%s\n",
				p.Ioa, p.Value, p.Qds, packet.Coa, p.Time.Format(time.RFC3339Nano))
			m.bridge.publishEvent(packet.CommonAddr, p.Ioa, packet.Type, packet.Coa, p)
		}
	case client.MeasuredValueNormal:
		for _, p := range packet.GetMeasuredValueNormal() {
			logf("[client] measured normal: IOA=%d value=%.6f QDS=%d COT=%v time=%s\n",
				p.Ioa, p.Value.Float64(), p.Qds, packet.Coa, p.Time.Format(time.RFC3339Nano))
			m.bridge.publishEvent(packet.CommonAddr, p.Ioa, packet.Type, packet.Coa, p)
		}
	case client.MeasuredValueFloat:
		for _, p := range packet.GetMeasuredValueFloat() {
			logf("[client] measured float: IOA=%d value=%.3f QDS=%d COT=%v type=%v time=%s\n",
				p.Ioa, p.Value, p.Qds, packet.Coa, packet.Type, p.Time.Format(time.RFC3339Nano))
			m.bridge.publishEvent(packet.CommonAddr, p.Ioa, packet.Type, packet.Coa, p)
		}
	case client.IntegratedTotals:
		for _, p := range packet.GetIntegratedTotals() {
			logf("[client] integrated totals: IOA=%d count=%d SQ=%d CY=%t CA=%t IV=%t COT=%v time=%s\n",
				p.Ioa, p.Value.CounterReading, p.Value.SeqNumber,
				p.Value.HasCarry, p.Value.IsAdjusted, p.Value.IsInvalid,
				packet.Coa, p.Time.Format(time.RFC3339Nano))
			m.bridge.publishEvent(packet.CommonAddr, p.Ioa, packet.Type, packet.Coa, p)
		}
	default:
		logf("[client] ASDU: type=%v COT=%v\n", packet.Type, packet.Coa)
	}
	return nil
}

// onCommandReply parses control command confirmations echoed by the server.
// It returns true when the frame was a command confirmation.
// (C_BO_NA_1 / C_BO_TA_1 confirmations carry a 32-bit bitstring value plus,
// for the TA variant, a CP56Time2a echo.)
func (m *masterCall) onCommandReply(packet *asdu.ASDU) bool {
	switch packet.Type {
	case asdu.C_TS_TA_1:
		addr, fbp, tm := packet.GetTestCommandCP56Time2a()
		logf("[client] test command reply (CP56): IOA=%d valid-test-word=%v COT=%v time=%s\n",
			addr, fbp, packet.Coa, tm.UTC().Format(time.RFC3339))
		m.bridge.publishEvent(packet.CommonAddr, addr, packet.Type, packet.Coa, fbp)
		return true
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		sc := packet.GetSingleCmd()
		logf("[client] single command confirmation: IOA=%d value=%v QOC=%d COT=%v\n",
			sc.Ioa, sc.Value, sc.Qoc.Qual, packet.Coa)
		m.bridge.publishEvent(packet.CommonAddr, sc.Ioa, packet.Type, packet.Coa, sc)
		return true
	case asdu.C_DC_NA_1, asdu.C_DC_TA_1:
		dc := packet.GetDoubleCmd()
		logf("[client] double command confirmation: IOA=%d value=%d QOC=%d COT=%v\n",
			dc.Ioa, dc.Value, dc.Qoc.Qual, packet.Coa)
		m.bridge.publishEvent(packet.CommonAddr, dc.Ioa, packet.Type, packet.Coa, dc)
		return true
	case asdu.C_SE_NA_1, asdu.C_SE_TA_1:
		sp := packet.GetSetpointNormalCmd()
		logf("[client] setpoint normal confirmation: IOA=%d value=%.6f COT=%v\n",
			sp.Ioa, sp.Value.Float64(), packet.Coa)
		m.bridge.publishEvent(packet.CommonAddr, sp.Ioa, packet.Type, packet.Coa, sp)
		return true
	case asdu.C_SE_NB_1, asdu.C_SE_TB_1:
		sp := packet.GetSetpointCmdScaled()
		logf("[client] setpoint scaled confirmation: IOA=%d value=%d COT=%v\n",
			sp.Ioa, sp.Value, packet.Coa)
		m.bridge.publishEvent(packet.CommonAddr, sp.Ioa, packet.Type, packet.Coa, sp)
		return true
	case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		sp := packet.GetSetpointFloatCmd()
		logf("[client] setpoint float confirmation: IOA=%d value=%.3f COT=%v\n",
			sp.Ioa, sp.Value, packet.Coa)
		m.bridge.publishEvent(packet.CommonAddr, sp.Ioa, packet.Type, packet.Coa, sp)
		return true
	case asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		bc := packet.GetBitsString32Cmd()
		logf("[client] bitstring32 command confirmation: IOA=%d value=0x%08X COT=%v time=%s\n",
			bc.Ioa, bc.Value, packet.Coa, bc.Time.UTC().Format(time.RFC3339))
		m.bridge.publishEvent(packet.CommonAddr, bc.Ioa, packet.Type, packet.Coa, bc)
		return true
	default:
		return false
	}
}
