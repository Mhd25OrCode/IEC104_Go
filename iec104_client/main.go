package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/iec104/client"
)

func main() {
	host := flag.String("host", "127.0.0.1", "server host")
	port := flag.Int("port", 2404, "server port")
	ca := flag.Uint("ca", 1, "ASDU common address")
	duration := flag.Duration("duration", 0, "how long to run (0 = until stdin closes or Ctrl+C/SIGTERM)")
	verbose := flag.Bool("verbose", false, "enable wendy512/go-iecp5 library logging (frames + decode errors), written to stderr")
	demo := flag.Bool("demo", false, "send the built-in demo command sequence once the outstation is active and skip the stdio bridge entirely (no stdin/stdout protocol)")
	showVersion := flag.Bool("version", false, "print build/version info and exit (does not start the bridge)")
	flag.Parse()

	// -version must answer on the real stdout and exit before stdout is
	// redirected to stderr for the protocol stream below.
	if *showVersion {
		fmt.Println(buildVersion())
		return
	}

	// stdout carries only NDJSON protocol frames (see IEC104_GO_BRIDGE.md).
	// Keep the real stdout for the bridge and point os.Stdout (and the standard
	// logger) at stderr so no stray print can corrupt the protocol stream.
	protoOut := os.Stdout
	os.Stdout = os.Stderr
	log.SetOutput(os.Stderr)

	os.Exit(run(*host, *port, uint16(*ca), *duration, *verbose, *demo, os.Stdin, protoOut))
}

// buildVersion is the single line printed by -version. It reports the module
// path, the VCS revision/branch stamp the Go toolchain embeds at build time
// (build.sh builds with -trimpath) and the NDJSON bridge protocol version, so a
// deployed binary can be told apart from an older one. A pre-bridge (gRPC) build
// has no bridge code at all and links google.golang.org/grpc, so it never prints
// the "ready" frame the MSL5 backend waits for.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Sprintf("iec104-client (no build info) bridge=ndjson/%d", protocolVersion)
	}
	rev, built, modified := "unknown", "unknown", "false"
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			built = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	return fmt.Sprintf(
		"iec104-client module=%s version=%s revision=%s built=%s modified=%s go=%s bridge=ndjson/%d",
		info.Main.Path, info.Main.Version, rev, built, modified, info.GoVersion, protocolVersion,
	)
}

// stderrLogger is the go-iecp5 LogProvider; the library's default logger
// writes to os.Stdout, which would corrupt the protocol stream.
type stderrLogger struct{ l *log.Logger }

func (s stderrLogger) Critical(format string, v ...any) { s.l.Printf("[C]: "+format, v...) }
func (s stderrLogger) Error(format string, v ...any)    { s.l.Printf("[E]: "+format, v...) }
func (s stderrLogger) Warn(format string, v ...any)     { s.l.Printf("[W]: "+format, v...) }
func (s stderrLogger) Debug(format string, v ...any)    { s.l.Printf("[D]: "+format, v...) }

func run(host string, port int, ca uint16, duration time.Duration, verbose, demo bool, in io.Reader, protoOut io.Writer) int {
	settings := client.NewSettings()
	settings.Host = host
	settings.Port = port
	settings.AutoConnect = true
	// See iec_60870_5_104_bug_report.md "Bug 3": the client swallows ASDU
	// decode errors (sf.Warn) unless this is enabled.
	settings.LogCfg = &client.LogCfg{
		Enable:      verbose,
		LogProvider: stderrLogger{log.New(os.Stderr, "cs104 client => ", log.LstdFlags)},
	}

	// With -demo the demo sequence is the only traffic source, so the stdio
	// bridge is not built at all: masterCall.bridge stays nil and inbound
	// events are logged to stderr only (bridge.publishEvent is nil-safe).
	var b *bridge
	if !demo {
		b = newBridge(nil) // client wired in below, once c exists
	}
	c := client.New(settings, &masterCall{bridge: b})
	if b != nil {
		b.setClient(c)
	}
	c.SetOnConnectHandler(func(_ *client.Client) {
		logf("[client] connected to outstation\n")
	})

	// The master traffic starts once the outstation has confirmed STARTDT.
	c.SetServerActiveHandler(func(c *client.Client) {
		logf("[client] outstation active (STARTDT confirmed)\n")
		if demo {
			logf("[client] -demo set - sending demo commands\n")
			sendDemoCommands(c, ca)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if duration > 0 {
		time.AfterFunc(duration, cancel)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
	}()

	// The client is initialised: announce readiness. The IEC 104 TCP session
	// does not have to be up yet; commands dispatched before it is are
	// rejected in their ack.
	if b != nil {
		b.start(protoOut)
	} else {
		log.Println("[client] -demo set: stdio bridge disabled, running demo sequence only")
	}

	var connected atomic.Bool
	connectDone := make(chan struct{})
	connErr := make(chan error, 1)
	go func() {
		defer close(connectDone)
		if !demo && !waitForOutstation(ctx, host, port) {
			return
		}
		if err := c.Connect(); err != nil {
			connErr <- err
			return
		}
		connected.Store(true)
		log.Printf("connected to %s:%d (common addr %d)", host, port, ca)
	}()

	serveErr := make(chan error, 1)
	if b != nil {
		go func() { serveErr <- b.serve(in) }()
	}

	exitCode := 0
	select {
	case <-ctx.Done():
		log.Println("shutdown requested")
	case err := <-serveErr:
		if err != nil {
			log.Printf("stdin: %v", err)
			exitCode = 1
		} else {
			log.Println("stdin closed, shutting down")
		}
	case err := <-connErr:
		log.Printf("connect failed: %v", err)
		exitCode = 1
	}

	cancel()
	select {
	case <-connectDone:
	case <-time.After(2 * time.Second):
	}
	if connected.Load() {
		if err := c.Close(); err != nil {
			log.Printf("close: %v", err)
		}
	}
	if b != nil {
		b.stop()
	}
	log.Println("client stopped")
	return exitCode
}

// waitForOutstation blocks until the outstation accepts TCP connections, so the
// bridge can announce "ready" and serve stdin while the outstation is still
// down. It returns false if ctx is cancelled first.
func waitForOutstation(ctx context.Context, host string, port int) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	for {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return true
		}
		log.Printf("waiting for outstation %s: %v", addr, err)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
}

// sendDemoCommands exercises every supported master-to-station command.
func sendDemoCommands(c *client.Client, ca uint16) {
	cmds := []struct {
		name string
		fn   func() error
	}{
		{"general interrogation", func() error { return c.SendInterrogationCmd(ca) }},
		{"counter interrogation", func() error { return c.SendCounterInterrogationCmd(ca) }},
		{"read command", func() error { return c.SendReadCmd(ca, 100) }},
		{"clock synchronization", func() error { return c.SendClockSynchronizationCmd(ca, time.Now()) }},
		{"test command", func() error { return c.SendTestCmd(ca) }},
		{"reset process", func() error { return c.SendResetProcessCmd(ca) }},
		{"single command", func() error {
			return c.SendCmd(ca, asdu.C_SC_NA_1, asdu.InfoObjAddr(1000), true)
		}},
		{"double command", func() error {
			return c.SendCmd(ca, asdu.C_DC_NA_1, asdu.InfoObjAddr(2000), uint8(asdu.DPIDeterminedOn))
		}},
		{"setpoint float", func() error {
			return c.SendCmd(ca, asdu.C_SE_NC_1, asdu.InfoObjAddr(3000), float32(125.5))
		}},
		{"bitstring command", func() error {
			return c.SendCmd(ca, asdu.C_BO_TA_1, asdu.InfoObjAddr(4000), uint32(0x00FF00FF))
		}},
		// C_BO_NA_1 (51) is the untagged twin of C_BO_TA_1 (64) and is the one
		// that survives the peer's decoder: go-iecp5's infoObjSize table has no
		// entry for C_BO_TA_1, so the peer drops that frame during
		// UnmarshalBinary ("type identification unknown") before any handler
		// runs - see iec_60870_5_104_bug_report.md "Bug 3".
		{"bitstring command (untagged twin of C_BO_TA_1)", func() error {
			return c.SendCmd(ca, asdu.C_BO_NA_1, asdu.InfoObjAddr(4001), uint32(0x0F0F0F0F))
		}},
	}
	for _, cmd := range cmds {
		if err := cmd.fn(); err != nil {
			logf("[client] ERROR sending %s: %v\n", cmd.name, err)
			continue
		}
		logf("[client] %s sent\n", cmd.name)
	}
}
