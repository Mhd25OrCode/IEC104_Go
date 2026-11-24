package main

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
)

const kickDebounce = 100 * time.Millisecond

// logSendError reports a failed send. Not-active / closed connections are
// expected around reconnects and are dropped silently.
func logSendError(what string, err error) {
	switch {
	case errors.Is(err, cs104.ErrNotActive), errors.Is(err, cs104.ErrUseClosedConnection):
		return
	case errors.Is(err, cs104.ErrBufferFulled):
		fmt.Printf("[client] warning: send buffer full, %s dropped\n", what)
	default:
		fmt.Printf("[client] %s send failed: %v\n", what, err)
	}
}

// reporter sends the process image to MSL5 as spontaneous frames while the
// session is active.
type reporter struct {
	st       *station
	conn     asdu.Connect
	ca       asdu.CommonAddr
	interval time.Duration
	cyclic   bool

	mu   sync.Mutex
	stop chan struct{} // nil = stopped
	done chan struct{}
	kick chan struct{}
}

func newReporter(st *station, conn asdu.Connect, ca asdu.CommonAddr, interval time.Duration, cyclic bool) *reporter {
	return &reporter{st: st, conn: conn, ca: ca, interval: interval, cyclic: cyclic, kick: make(chan struct{}, 1)}
}

// Start begins reporting; it is a no-op while already running.
func (r *reporter) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop != nil {
		return
	}
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	go r.loop(r.stop, r.done)
}

// Stop ends reporting and waits for the loop to exit; it is a no-op while stopped.
func (r *reporter) Stop() {
	r.mu.Lock()
	stop, done := r.stop, r.done
	r.stop, r.done = nil, nil
	r.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

// Kick requests a debounced report; it never blocks and is ignored while stopped.
func (r *reporter) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

func (r *reporter) loop(stop, done chan struct{}) {
	defer close(done)
	// Drop a stale kick from an earlier session: the full report below covers it.
	select {
	case <-r.kick:
	default:
	}
	r.reportAll()

	var tick <-chan time.Time
	if r.interval > 0 {
		t := time.NewTicker(r.interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-stop:
			return
		case <-tick:
			r.st.mu.Lock()
			r.st.counter++
			r.st.mu.Unlock()
			r.reportAll()
		case <-r.kick:
			select {
			case <-stop:
				return
			case <-time.After(kickDebounce):
			}
			r.reportAll()
		}
	}
}

// reportAll sends the six spontaneous frames. The image is snapshotted first;
// st.mu is never held while sending. A failed send aborts the round.
func (r *reporter) reportAll() {
	r.st.mu.Lock()
	sp, dp, ana, counter := r.st.spVal, r.st.dpVal, r.st.ana, r.st.counter
	r.st.mu.Unlock()

	// MSL5 never writes double points 2/3, report 0/1 only.
	if dp != asdu.DPIDeterminedOff {
		dp = asdu.DPIDeterminedOn
	}
	spont := coa(asdu.Spontaneous)
	measCOA := spont
	if r.cyclic {
		measCOA = coa(asdu.Periodic)
	}
	ca := r.ca

	frames := []struct {
		name string
		send func() error
	}{
		{"M_SP_NA_1", func() error {
			return asdu.Single(r.conn, false, spont, ca, asdu.SinglePointInfo{Ioa: 100, Value: sp, Qds: asdu.QDSGood})
		}},
		{"M_DP_NA_1", func() error {
			return asdu.Double(r.conn, false, spont, ca, asdu.DoublePointInfo{Ioa: 200, Value: dp, Qds: asdu.QDSGood})
		}},
		{"M_ME_NC_1", func() error {
			return asdu.MeasuredValueFloat(r.conn, false, measCOA, ca, asdu.MeasuredValueFloatInfo{Ioa: 301, Value: ana, Qds: asdu.QDSGood})
		}},
		{"M_ME_TF_1", func() error {
			return sendMeasuredFloatTime(r.conn, asdu.Spontaneous, ca,
				asdu.MeasuredValueFloatInfo{Ioa: 302, Value: ana, Qds: asdu.QDSGood, Time: time.Now()})
		}},
		{"M_ME_NB_1", func() error {
			return asdu.MeasuredValueScaled(r.conn, false, measCOA, ca, asdu.MeasuredValueScaledInfo{Ioa: 303, Value: clampInt16(ana), Qds: asdu.QDSGood})
		}},
		{"M_IT_NA_1", func() error {
			return sendCounterRequest(r.conn, asdu.Spontaneous, ca, 400,
				asdu.BinaryCounterReading{CounterReading: counter, SeqNumber: 1})
		}},
	}
	for _, f := range frames {
		if err := f.send(); err != nil {
			logSendError(f.name, err)
			return
		}
	}
}

func clampInt16(v float32) int16 {
	switch {
	case v >= 32767:
		return 32767
	case v <= -32768:
		return -32768
	}
	return int16(v)
}
