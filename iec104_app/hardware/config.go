package hardware

import (
	"log/slog"
	"time"
)

// Config describes the outstation the hardware layer talks to.
type Config struct {
	Host string
	Port int

	// Verbose enables go-iecp5 frame/decode-error logging (see
	// iec_60870_5_104_bug_report.md "Bug 3": decode errors are swallowed
	// unless this is on).
	Verbose bool

	// Logger receives diagnostics; nil discards them.
	Logger *slog.Logger

	// EventBuffer is the capacity of the Events channel (default 1024).
	EventBuffer int

	// RetryInterval is the delay between outstation reachability probes
	// (default 2s).
	RetryInterval time.Duration

	// OnActive, if set, runs once the outstation has confirmed STARTDT.
	OnActive func()
}

func (c *Config) applyDefaults() {
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	if c.EventBuffer <= 0 {
		c.EventBuffer = 1024
	}
	if c.RetryInterval <= 0 {
		c.RetryInterval = 2 * time.Second
	}
}
