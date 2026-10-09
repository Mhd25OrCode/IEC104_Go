package hardware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/iec104/client"
)

// ErrNotConnected is returned by Send before the IEC 104 session is up.
var ErrNotConnected = errors.New("hardware: not connected to outstation")

// Layer is the IEC 104 master hardware layer: it owns the connection to one
// outstation, sends Commands and publishes decoded Events.
type Layer struct {
	cfg    Config
	log    *slog.Logger
	reg    *registry
	events chan Event
	cmd    atomic.Pointer[commander]
}

// New builds a Layer; call Run to connect.
func New(cfg Config) *Layer {
	cfg.applyDefaults()
	return &Layer{
		cfg:    cfg,
		log:    cfg.Logger,
		reg:    newRegistry(),
		events: make(chan Event, cfg.EventBuffer),
	}
}

// Events returns the stream of decoded outstation messages. It is never
// closed; stop reading when Run returns. When the buffer is full new events are
// dropped so the IEC 104 library's handler goroutine never blocks.
func (l *Layer) Events() <-chan Event { return l.events }

// Register associates meta with an address without sending anything, so later
// events for it carry that tag (useful for monitoring points).
func (l *Layer) Register(ca asdu.CommonAddr, ioa asdu.InfoObjAddr, meta any) {
	l.reg.store(ca, ioa, meta)
}

// Send transmits one command. It fails with ErrNotConnected until Run has
// established the session.
func (l *Layer) Send(cmd Command) error {
	c := l.cmd.Load()
	if c == nil {
		return ErrNotConnected
	}
	l.reg.store(cmd.CA, cmd.IOA, cmd.Meta)
	return dispatch(*c, cmd)
}

// SendAll sends every command and returns all failures joined; one failing
// command does not stop the rest.
func (l *Layer) SendAll(cmds []Command) error {
	var errs []error
	for _, cmd := range cmds {
		if err := l.Send(cmd); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", typeNameFromID(cmd.Type), err))
		}
	}
	return errors.Join(errs...)
}

// Run waits for the outstation, connects and blocks until ctx is cancelled,
// then closes the connection. It returns an error if the connection fails.
func (l *Layer) Run(ctx context.Context) error {
	settings := client.NewSettings()
	settings.Host = l.cfg.Host
	settings.Port = l.cfg.Port
	settings.AutoConnect = true
	// See iec_60870_5_104_bug_report.md "Bug 3": the client swallows ASDU
	// decode errors unless logging is enabled.
	settings.LogCfg = &client.LogCfg{Enable: l.cfg.Verbose, LogProvider: libLogger{l.log}}

	c := client.New(settings, &masterCall{emit: l.publish})
	c.SetOnConnectHandler(func(*client.Client) { l.log.Info("connected to outstation") })
	c.SetServerActiveHandler(func(*client.Client) {
		l.log.Info("outstation active (STARTDT confirmed)")
		if l.cfg.OnActive != nil {
			l.cfg.OnActive()
		}
	})

	if !waitForOutstation(ctx, l.cfg.Host, l.cfg.Port, l.cfg.RetryInterval, l.log) {
		return nil
	}
	// Stored before Connect: the active handler may fire before it returns.
	var cmd commander = c
	l.cmd.Store(&cmd)
	if err := c.Connect(); err != nil {
		l.cmd.Store(nil)
		return fmt.Errorf("connect: %w", err)
	}

	<-ctx.Done()

	l.cmd.Store(nil)
	if err := c.Close(); err != nil {
		l.log.Warn("close", "err", err)
	}
	return nil
}

// publish builds the Event for one decoded message and queues it without
// blocking.
func (l *Layer) publish(ca asdu.CommonAddr, ioa asdu.InfoObjAddr, typeId asdu.TypeID, cot asdu.CauseOfTransmission, value any) {
	name := typeNameFromID(typeId)
	ev := Event{
		CA: ca, IOA: ioa, Type: typeId, TypeName: name,
		Direction: directionOf(name),
		COT:       cot,
		Value:     value,
		Meta:      l.reg.lookup(ca, ioa),
		Time:      time.Now(),
	}
	select {
	case l.events <- ev:
	default:
		l.log.Warn("event buffer full, dropping event", "type", name, "ioa", ioa)
	}
}
