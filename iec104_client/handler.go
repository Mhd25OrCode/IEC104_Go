package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

// pointKey identifies a monitored point; MSL5 rows carry their own CA/IOA.
type pointKey struct {
	CA  asdu.CommonAddr
	IOA asdu.InfoObjAddr
}

// monitoringValue is the last M_* value received from MSL5 for a point.
type monitoringValue struct {
	Type  asdu.TypeID
	Value any // bool | uint8 | float32 | int16 | int32
	Qds   asdu.QualityDescriptor
	Time  time.Time // zero for the _NA_1 variants
	At    time.Time // receive time
}

// confirmation records the last COT 7 / COT 10 seen for a command point.
type confirmation struct {
	Cause asdu.Cause
	At    time.Time
}

// station holds what the client learns from MSL5: the monitoring image
// (M_* frames) and command confirmations. It is read-only data; nothing
// received here ever triggers an outgoing frame.
type station struct {
	mu            sync.Mutex
	monitoring    map[pointKey]monitoringValue
	confirmations map[pointKey]confirmation
	logFrames     bool
}

func newStation(logFrames bool) *station {
	return &station{
		monitoring:    make(map[pointKey]monitoringValue),
		confirmations: make(map[pointKey]confirmation),
		logFrames:     logFrames,
	}
}

func (st *station) logf(format string, args ...any) {
	if st.logFrames {
		fmt.Printf("[client] "+format, args...)
	}
}

// Snapshot returns a copy of the monitoring image.
func (st *station) Snapshot() map[pointKey]monitoringValue {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make(map[pointKey]monitoringValue, len(st.monitoring))
	for k, v := range st.monitoring {
		out[k] = v
	}
	return out
}

// Confirmation returns the last confirmation recorded for a point.
func (st *station) Confirmation(k pointKey) (confirmation, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	c, ok := st.confirmations[k]
	return c, ok
}

// CommandValue returns the outgoing value of a command point of type t from the
// last monitoring frame received for the same CA + IOA. Only a monitoring type
// that maps onto the command type is reported, so e.g. an M_ME_NC_1 at IOA 301
// never feeds a C_SE_NA_1 and an M_IT_NA_1 counter never feeds a C_BO_NA_1 bit
// string. The image is only read: a received frame still never triggers an
// outgoing frame, the value is used by the next send round.
func (st *station) CommandValue(ca asdu.CommonAddr, ioa asdu.InfoObjAddr, t asdu.TypeID) (any, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	v, ok := st.monitoring[pointKey{ca, ioa}]
	if !ok {
		return nil, false
	}
	switch t {
	case asdu.C_SC_NA_1:
		if v.Type == asdu.M_SP_NA_1 {
			b, ok := v.Value.(bool)
			return b, ok
		}
	case asdu.C_DC_NA_1:
		if v.Type == asdu.M_DP_NA_1 {
			d, ok := v.Value.(uint8)
			return d, ok
		}
	case asdu.C_SE_NB_1:
		if v.Type == asdu.M_ME_NB_1 {
			i, ok := v.Value.(int16)
			return i, ok
		}
	case asdu.C_SE_NC_1:
		if v.Type == asdu.M_ME_NC_1 || v.Type == asdu.M_ME_TF_1 {
			f, ok := v.Value.(float32)
			return f, ok
		}
	}
	return nil, false
}

// OnASDU handles frames routed through ASDUHandler. Monitoring frames update
// the image; COT 7/10 are recorded as confirmations; everything else is only
// logged (with -log-frames). It never sends and never returns an error: a
// handler error would make the library mirror the frame back.
func (st *station) OnASDU(_ asdu.Connect, pack *asdu.ASDU) error {
	ca := pack.CommonAddr
	cot := pack.Identifier.Coa

	if isCommandType(pack.Type) {
		st.recordCommandFrame(pack)
		return nil
	}

	switch pack.Type {
	case asdu.M_SP_NA_1:
		for _, o := range pack.GetSinglePoint() {
			st.put(cot, ca, o.Ioa, monitoringValue{Type: pack.Type, Value: o.Value, Qds: o.Qds, Time: o.Time})
		}
		return nil
	case asdu.M_DP_NA_1:
		for _, o := range pack.GetDoublePoint() {
			st.put(cot, ca, o.Ioa, monitoringValue{Type: pack.Type, Value: uint8(o.Value), Qds: o.Qds, Time: o.Time})
		}
		return nil
	case asdu.M_ME_NC_1, asdu.M_ME_TF_1:
		for _, o := range pack.GetMeasuredValueFloat() {
			st.put(cot, ca, o.Ioa, monitoringValue{Type: pack.Type, Value: o.Value, Qds: o.Qds, Time: o.Time})
		}
		return nil
	case asdu.M_ME_NB_1:
		for _, o := range pack.GetMeasuredValueScaled() {
			st.put(cot, ca, o.Ioa, monitoringValue{Type: pack.Type, Value: o.Value, Qds: o.Qds, Time: o.Time})
		}
		return nil
	case asdu.M_IT_NA_1:
		for _, o := range pack.GetIntegratedTotals() {
			st.put(cot, ca, o.Ioa, monitoringValue{Type: pack.Type, Value: o.Value.CounterReading, Time: o.Time})
		}
		return nil
	}
	st.logf("RX unhandled ASDU: %v %v CA=%d\n", pack.Type, cot, ca)
	return nil
}

func (st *station) put(cot asdu.CauseOfTransmission, ca asdu.CommonAddr, ioa asdu.InfoObjAddr, v monitoringValue) {
	v.At = time.Now()
	st.mu.Lock()
	st.monitoring[pointKey{ca, ioa}] = v
	st.mu.Unlock()
	if v.Time.IsZero() {
		st.logf("RX %v %v CA=%d IOA=%d value=%v qds=0x%02X\n", v.Type, cot, ca, ioa, v.Value, byte(v.Qds))
		return
	}
	st.logf("RX %v %v CA=%d IOA=%d value=%v qds=0x%02X time=%s\n", v.Type, cot, ca, ioa, v.Value, byte(v.Qds), v.Time.Format(time.RFC3339Nano))
}

func isCommandType(t asdu.TypeID) bool {
	switch t {
	case asdu.C_SC_NA_1, asdu.C_DC_NA_1, asdu.C_RC_NA_1, asdu.C_SE_NA_1, asdu.C_SE_NB_1, asdu.C_SE_NC_1, asdu.C_BO_NA_1:
		return true
	}
	return false
}

// recordCommandFrame handles a received C_* frame. In this contract VK is the
// controlling station: only confirmations (COT 7/10) are expected and they are
// recorded, never executed or answered. Anything else, including COT 6, is
// ignored — executing an echoed command would create a command loop.
func (st *station) recordCommandFrame(pack *asdu.ASDU) {
	ca := pack.CommonAddr
	cause := pack.Identifier.Coa.Cause
	if cause != asdu.ActivationCon && cause != asdu.ActivationTerm {
		st.logf("RX ignored command frame: %v %v CA=%d\n", pack.Type, pack.Identifier.Coa, ca)
		return
	}
	ioa := commandFrameIOA(pack)
	st.mu.Lock()
	st.confirmations[pointKey{ca, ioa}] = confirmation{Cause: cause, At: time.Now()}
	st.mu.Unlock()
	st.logf("RX confirmation: %v %v CA=%d IOA=%d\n", pack.Type, pack.Identifier.Coa, ca, ioa)
}

func commandFrameIOA(p *asdu.ASDU) asdu.InfoObjAddr {
	switch p.Type {
	case asdu.C_SC_NA_1:
		return p.GetSingleCmd().Ioa
	case asdu.C_DC_NA_1:
		return p.GetDoubleCmd().Ioa
	case asdu.C_RC_NA_1:
		return p.GetStepCmd().Ioa
	case asdu.C_SE_NA_1:
		return p.GetSetpointNormalCmd().Ioa
	case asdu.C_SE_NB_1:
		return p.GetSetpointCmdScaled().Ioa
	case asdu.C_SE_NC_1:
		return p.GetSetpointFloatCmd().Ioa
	case asdu.C_BO_NA_1:
		return p.GetBitsString32Cmd().Ioa
	}
	return asdu.InfoObjAddrIrrelevant
}
