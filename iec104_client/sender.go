package main

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
)

const (
	kickDebounce = 100 * time.Millisecond
	warnInterval = 10 * time.Second
)

var (
	warnMu   sync.Mutex
	lastWarn time.Time
)

// logSendError reports a failed send. Not-active / closed connections are
// expected around reconnects and are dropped silently; a full buffer is
// reported at most once per warnInterval.
func logSendError(what string, err error) {
	switch {
	case errors.Is(err, cs104.ErrNotActive), errors.Is(err, cs104.ErrUseClosedConnection):
		return
	case errors.Is(err, cs104.ErrBufferFulled):
		warnMu.Lock()
		due := time.Since(lastWarn) >= warnInterval
		if due {
			lastWarn = time.Now()
		}
		warnMu.Unlock()
		if due {
			fmt.Printf("[client] warning: send buffer full, %s dropped\n", what)
		}
	default:
		fmt.Printf("[client] %s send failed: %v\n", what, err)
	}
}

// commandPoint is one configured outgoing command. Value must match the type:
// bool (C_SC), uint8 (C_DC, C_RC), int16 (C_SE_NA, C_SE_NB), float32 (C_SE_NC),
// uint32 (C_BO).
type commandPoint struct {
	Type  asdu.TypeID
	IOA   asdu.InfoObjAddr
	Value any
}

// defaultCommandPoints covers the seven command types MSL5 accepts.
func defaultCommandPoints() []commandPoint {
	return []commandPoint{
		{asdu.C_SC_NA_1, 100, false},
		{asdu.C_DC_NA_1, 200, uint8(1)},
		{asdu.C_RC_NA_1, 250, uint8(1)},
		{asdu.C_SE_NA_1, 301, int16(0)},
		{asdu.C_SE_NB_1, 303, int16(0)},
		{asdu.C_SE_NC_1, 302, float32(0)},
		{asdu.C_BO_NA_1, 400, uint32(0)},
	}
}

// sender sends the configured commands to MSL5 (COT Activation) while the
// session is active.
type sender struct {
	conn      asdu.Connect
	ca        asdu.CommonAddr
	interval  time.Duration
	onChange  bool
	logFrames bool
	image     *station // optional process image: last M_* value per CA + IOA

	mu     sync.Mutex // guards points, stop, done
	points []commandPoint
	stop   chan struct{} // nil = stopped
	done   chan struct{}
	kick   chan struct{}
}

func newSender(conn asdu.Connect, ca asdu.CommonAddr, points []commandPoint, interval time.Duration, onChange, logFrames bool, image *station) *sender {
	return &sender{
		conn:      conn,
		ca:        ca,
		points:    append([]commandPoint(nil), points...),
		interval:  interval,
		onChange:  onChange,
		logFrames: logFrames,
		image:     image,
		kick:      make(chan struct{}, 1),
	}
}

// Set changes the value of a configured point and, if send-on-change is
// enabled, requests a debounced send. It reports whether the point exists.
func (s *sender) Set(ioa asdu.InfoObjAddr, v any) bool {
	s.mu.Lock()
	found := false
	for i := range s.points {
		if s.points[i].IOA == ioa {
			s.points[i].Value = v
			found = true
		}
	}
	s.mu.Unlock()
	if found && s.onChange {
		s.Kick()
	}
	return found
}

// Start begins sending; it is a no-op while already running.
func (s *sender) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.loop(s.stop, s.done)
}

// Stop ends sending and waits for the loop to exit; it is a no-op while stopped.
func (s *sender) Stop() {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

// Kick requests a debounced send of all commands; it never blocks.
func (s *sender) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *sender) loop(stop, done chan struct{}) {
	defer close(done)
	// Drop a stale kick from an earlier session: with a period the round below
	// covers it; without one, send-on-change starts from the next change.
	select {
	case <-s.kick:
	default:
	}

	var tick <-chan time.Time
	if s.interval > 0 {
		s.sendAll()
		t := time.NewTicker(s.interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-stop:
			return
		case <-tick:
			s.sendAll()
		case <-s.kick:
			select {
			case <-stop:
				return
			case <-time.After(kickDebounce):
			}
			s.sendAll()
		}
	}
}

// sendAll sends one command per point. The points are snapshotted first; the
// mutex is never held while sending. Each value comes from the monitoring image
// when MSL5 reported one for the same CA + IOA. A failed send aborts the round.
func (s *sender) sendAll() {
	s.mu.Lock()
	points := append([]commandPoint(nil), s.points...)
	s.mu.Unlock()

	act := coa(asdu.Activation)
	for _, p := range points {
		v := s.commandValue(p)
		p.Value = v
		if err := s.send(p); err != nil {
			if errors.Is(err, asdu.ErrTypeIDNotMatch) {
				s.logf("command %v IOA=%d rejected by builder: %v\n", p.Type, p.IOA, err)
				continue
			}
			logSendError(p.Type.String(), err)
			return
		}
		s.logf("TX %v %v CA=%d IOA=%d value=%v\n", p.Type, act, s.ca, p.IOA, v)
	}
}

// commandValue returns the value to send for p: the last monitoring value
// received from MSL5 for the same CA + IOA when it maps onto the command type,
// otherwise the configured value. Received frames never trigger a send; the
// next round picks the new value up.
func (s *sender) commandValue(p commandPoint) any {
	if s.image != nil {
		if v, ok := s.image.CommandValue(s.ca, p.IOA, p.Type); ok {
			return v
		}
	}
	return p.Value
}

func (s *sender) logf(format string, args ...any) {
	if s.logFrames {
		fmt.Printf("[client] "+format, args...)
	}
}

// send builds a fresh ASDU for p and sends it with COT Activation.
func (s *sender) send(p commandPoint) error {
	act := coa(asdu.Activation)
	switch p.Type {
	case asdu.C_SC_NA_1:
		v, _ := p.Value.(bool)
		return asdu.SingleCmd(s.conn, p.Type, act, s.ca, asdu.SingleCommandInfo{Ioa: p.IOA, Value: v})
	case asdu.C_DC_NA_1:
		v, _ := p.Value.(uint8)
		return asdu.DoubleCmd(s.conn, p.Type, act, s.ca, asdu.DoubleCommandInfo{Ioa: p.IOA, Value: asdu.DoubleCommand(s.clamp2bit(p, v))})
	case asdu.C_RC_NA_1:
		v, _ := p.Value.(uint8)
		return asdu.StepCmd(s.conn, p.Type, act, s.ca, asdu.StepCommandInfo{Ioa: p.IOA, Value: asdu.StepCommand(s.clamp2bit(p, v))})
	case asdu.C_SE_NA_1:
		v, _ := p.Value.(int16)
		return asdu.SetpointCmdNormal(s.conn, p.Type, act, s.ca, asdu.SetpointCommandNormalInfo{Ioa: p.IOA, Value: asdu.Normalize(v)})
	case asdu.C_SE_NB_1:
		v, _ := p.Value.(int16)
		return asdu.SetpointCmdScaled(s.conn, p.Type, act, s.ca, asdu.SetpointCommandScaledInfo{Ioa: p.IOA, Value: v})
	case asdu.C_SE_NC_1:
		v, _ := p.Value.(float32)
		return asdu.SetpointCmdFloat(s.conn, p.Type, act, s.ca, asdu.SetpointCommandFloatInfo{Ioa: p.IOA, Value: v})
	case asdu.C_BO_NA_1:
		v, _ := p.Value.(uint32)
		return asdu.BitsString32Cmd(s.conn, p.Type, act, s.ca, asdu.BitsString32CommandInfo{Ioa: p.IOA, Value: v})
	}
	return asdu.ErrTypeIDNotMatch
}

// clamp2bit limits a double/step command value to 0..3.
func (s *sender) clamp2bit(p commandPoint, v uint8) uint8 {
	if v > 3 {
		s.logf("%v IOA=%d value %d out of range, clamped to 3\n", p.Type, p.IOA, v)
		return 3
	}
	return v
}
