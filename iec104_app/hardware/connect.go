package hardware

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"
)

// libLogger adapts slog to the go-iecp5 LogProvider interface; the library's
// default logger writes to os.Stdout.
type libLogger struct{ l *slog.Logger }

func (s libLogger) Critical(format string, v ...any) { s.l.Error(fmt.Sprintf(format, v...)) }
func (s libLogger) Error(format string, v ...any)    { s.l.Error(fmt.Sprintf(format, v...)) }
func (s libLogger) Warn(format string, v ...any)     { s.l.Warn(fmt.Sprintf(format, v...)) }
func (s libLogger) Debug(format string, v ...any)    { s.l.Debug(fmt.Sprintf(format, v...)) }

// waitForOutstation blocks until the outstation accepts TCP connections, so the
// layer stays usable (and cancellable) while the outstation is still down. It
// returns false if ctx is cancelled first.
func waitForOutstation(ctx context.Context, host string, port int, retry time.Duration, log *slog.Logger) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var d net.Dialer
	for {
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := d.DialContext(dialCtx, "tcp", addr)
		cancel()
		if err == nil {
			conn.Close()
			return true
		}
		log.Info("waiting for outstation", "addr", addr, "err", err)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(retry):
		}
	}
}
