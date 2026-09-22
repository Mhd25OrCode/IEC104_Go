package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

// station implements server.CommandHandler and simulates an outstation
// ("slave") with a small process image and background reporting.
type station struct {
	ca asdu.CommonAddr

	mu      sync.Mutex
	spVal   bool
	dpVal   asdu.DoublePoint
	ana     float32
	counter int32

	started sync.Map // remote addr string -> struct{}: one reporting pair per live session
	period  time.Duration
	spont   time.Duration
}

// startReporting launches periodic and spontaneous reporting for a session.
// It is idempotent per connection address and is invoked from OnInterrogation,
// i.e. only after the STARTDT handshake is complete.
func (st *station) startReporting(conn asdu.Connect) {
	key := conn.UnderlyingConn().RemoteAddr().String()
	if _, loaded := st.started.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	go st.periodicLoop(conn)
	go st.spontaneousLoop(conn)
}

// periodicLoop reports a slowly drifting analog measurement (M_ME_NC_1, COT=1).
func (st *station) periodicLoop(conn asdu.Connect) {
	fails := 0
	for {
		time.Sleep(st.period)
		st.mu.Lock()
		st.ana += 12.5
		if st.ana > 220 {
			st.ana = -30
		}
		ana := st.ana
		st.mu.Unlock()
		err := asdu.MeasuredValueFloat(conn, false, coa(asdu.Periodic), st.ca,
			asdu.MeasuredValueFloatInfo{Ioa: 301, Value: ana, Qds: asdu.QDSGood})
		if err != nil {
			fails++
			if fails > 30 {
				return
			}
			continue
		}
		fails = 0
	}
}

// spontaneousLoop toggles a binary input and reports it (M_SP_TB_1, COT=3).
// The flip-flop is owned by this loop so reporting alternates deterministically.
func (st *station) spontaneousLoop(conn asdu.Connect) {
	fails := 0
	v := st.spVal
	for {
		time.Sleep(st.spont)
		v = !v
		st.mu.Lock()
		st.spVal = v
		st.mu.Unlock()
		err := asdu.SingleCP56Time2a(conn, coa(asdu.Spontaneous), st.ca, asdu.SinglePointInfo{
			Ioa:   100,
			Value: v,
			Qds:   asdu.QDSGood,
			Time:  time.Now(),
		})
		if err != nil {
			fails++
			if fails > 30 {
				return
			}
			continue
		}
		fails = 0
	}
}

// sendAllData reports the whole process image with COT=20 (station
// interrogation response).
func (st *station) sendAllData(conn asdu.Connect) error {
	st.mu.Lock()
	sp, dp, ana := st.spVal, st.dpVal, st.ana
	st.mu.Unlock()

	if err := asdu.Single(conn, false, coa(asdu.InterrogatedByStation), st.ca,
		asdu.SinglePointInfo{Ioa: 100, Value: sp, Qds: asdu.QDSGood}); err != nil {
		return err
	}
	if err := asdu.Double(conn, false, coa(asdu.InterrogatedByStation), st.ca,
		asdu.DoublePointInfo{Ioa: 200, Value: dp, Qds: asdu.QDSGood}); err != nil {
		return err
	}
	if err := asdu.MeasuredValueNormal(conn, false, coa(asdu.InterrogatedByStation), st.ca,
		asdu.MeasuredValueNormalInfo{Ioa: 300, Value: asdu.Normalize(10000), Qds: asdu.QDSGood}); err != nil {
		return err
	}
	return asdu.MeasuredValueFloat(conn, false, coa(asdu.InterrogatedByStation), st.ca,
		asdu.MeasuredValueFloatInfo{Ioa: 301, Value: ana, Qds: asdu.QDSGood})
}

// sendCounters reports integrated totals with COT=37 (response to a general
// counter interrogation).
func (st *station) sendCounters(conn asdu.Connect) error {
	st.mu.Lock()
	c := st.counter
	st.mu.Unlock()
	return asdu.IntegratedTotals(conn, false, coa(asdu.RequestByGeneralCounter), st.ca,
		asdu.BinaryCounterReadingInfo{
			Ioa:   400,
			Value: asdu.BinaryCounterReading{CounterReading: c, SeqNumber: 1},
		})
}

// OnInterrogation implements the standard sequence:
//   ActivationCon -> interrogated data -> ActivationTerm
func (st *station) OnInterrogation(conn asdu.Connect, pack *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	fmt.Printf("[server] general interrogation: COT=%v QOI=%d\n", pack.Identifier.Coa.Cause, qoi)
	st.startReporting(conn)
	if pack.Identifier.Coa.Cause == asdu.Deactivation {
		return confirmInterrogation(conn, asdu.DeactivationCon, st.ca, qoi)
	}
	var firstErr error
	if err := confirmInterrogation(conn, asdu.ActivationCon, st.ca, qoi); err != nil {
		firstErr = err
	}
	if err := st.sendAllData(conn); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := confirmInterrogation(conn, asdu.ActivationTerm, st.ca, qoi); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// OnCounterInterrogation responds with ActivationCon, the counters and
// ActivationTerm.
func (st *station) OnCounterInterrogation(conn asdu.Connect, pack *asdu.ASDU, qcc asdu.QualifierCountCall) error {
	fmt.Printf("[server] counter interrogation: COT=%v QCC=0x%02X\n", pack.Identifier.Coa.Cause, qcc.Value())
	if err := confirmCounterInterrogation(conn, asdu.ActivationCon, st.ca, qcc); err != nil {
		return err
	}
	if err := st.sendCounters(conn); err != nil {
		return err
	}
	return confirmCounterInterrogation(conn, asdu.ActivationTerm, st.ca, qcc)
}

// OnRead answers a read command with the current value (COT=5).
func (st *station) OnRead(conn asdu.Connect, _ *asdu.ASDU, addr asdu.InfoObjAddr) error {
	fmt.Printf("[server] read command: IOA=%d\n", addr)
	st.mu.Lock()
	v := st.spVal
	st.mu.Unlock()
	return asdu.Single(conn, false, coa(asdu.Request), st.ca,
		asdu.SinglePointInfo{Ioa: addr, Value: v, Qds: asdu.QDSGood})
}

// OnClockSync acknowledges a clock synchronization command.
func (st *station) OnClockSync(conn asdu.Connect, _ *asdu.ASDU, tm time.Time) error {
	fmt.Printf("[server] clock sync request: %v\n", tm.UTC())
	return confirmClockSync(conn, st.ca, tm)
}

// OnResetProcess acknowledges a reset-process command.
func (st *station) OnResetProcess(conn asdu.Connect, _ *asdu.ASDU, qrp asdu.QualifierOfResetProcessCmd) error {
	fmt.Printf("[server] reset process command: QRP=0x%02X\n", qrp)
	return confirmResetProcess(conn, st.ca, qrp)
}

// OnDelayAcquisition acknowledges a delay-acquisition command.
func (st *station) OnDelayAcquisition(conn asdu.Connect, _ *asdu.ASDU, msec uint16) error {
	fmt.Printf("[server] delay acquisition command: %d ms\n", msec)
	return confirmDelayAcquire(conn, st.ca, msec)
}

// OnTestCommand acknowledges a time-tagged test command.
func (st *station) OnTestCommand(conn asdu.Connect, _ *asdu.ASDU) error {
	fmt.Println("[server] test command (C_TS_NA_1)")
	return confirmTestCommand(conn, st.ca, time.Now())
}

// OnASDU handles control commands routed through ASDUHandler and echoes a
// fresh ActivationCon confirmation for each of them.
func (st *station) OnASDU(conn asdu.Connect, pack *asdu.ASDU) error {
	switch pack.Type {
	case asdu.C_TS_TA_1:
		fmt.Println("[server] test command (C_TS_TA_1)")
		return confirmTestCommand(conn, st.ca, time.Now())
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		sc := pack.GetSingleCmd()
		fmt.Printf("[server] single command: IOA=%d value=%v COT=%v\n", sc.Ioa, sc.Value, pack.Identifier.Coa)
		return confirmSingleCmd(conn, pack.Type, st.ca, sc)
	case asdu.C_DC_NA_1, asdu.C_DC_TA_1:
		dc := pack.GetDoubleCmd()
		fmt.Printf("[server] double command: IOA=%d value=%d COT=%v\n", dc.Ioa, dc.Value, pack.Identifier.Coa)
		return confirmDoubleCmd(conn, pack.Type, st.ca, dc)
	case asdu.C_SE_NA_1, asdu.C_SE_TA_1:
		sp := pack.GetSetpointNormalCmd()
		fmt.Printf("[server] setpoint normal command: IOA=%d value=%v COT=%v\n", sp.Ioa, sp.Value, pack.Identifier.Coa)
		return confirmSetpointNormal(conn, pack.Type, st.ca, sp)
	case asdu.C_SE_NB_1, asdu.C_SE_TB_1:
		sp := pack.GetSetpointCmdScaled()
		fmt.Printf("[server] setpoint scaled command: IOA=%d value=%v COT=%v\n", sp.Ioa, sp.Value, pack.Identifier.Coa)
		return confirmSetpointScaled(conn, pack.Type, st.ca, sp)
	case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		sp := pack.GetSetpointFloatCmd()
		fmt.Printf("[server] setpoint float command: IOA=%d value=%v COT=%v\n", sp.Ioa, sp.Value, pack.Identifier.Coa)
		return confirmSetpointFloat(conn, pack.Type, st.ca, sp)
	default:
		fmt.Printf("[server] unhandled ASDU: type=%v COT=%v\n", pack.Type, pack.Identifier.Coa)
		return nil
	}
}