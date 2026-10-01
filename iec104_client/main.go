package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/iec104/client"
	"google.golang.org/grpc"
)

func main() {
	host := flag.String("host", "127.0.0.1", "server host")
	port := flag.Int("port", 2404, "server port")
	ca := flag.Uint("ca", 1, "ASDU common address")
	duration := flag.Duration("duration", 30*time.Second, "how long to run (0 = until Ctrl+C)")
	verbose := flag.Bool("verbose", false, "enable wendy512/go-iecp5 library logging (frames + decode errors)")
	grpcSocket := flag.String("grpc-socket", "iec104-bridge.sock", "Unix Domain Socket path for the Iec104Bridge gRPC server (ignored with -demo; NestJS default: /run/msl5/iec104-bridge.sock)")
	demo := flag.Bool("demo", false, "send the built-in demo command sequence once the outstation is active and skip the gRPC server entirely (no Dispatch/SubscribeEvents)")
	flag.Parse()

	settings := client.NewSettings()
	settings.Host = *host
	settings.Port = *port
	settings.AutoConnect = true
	if *verbose {
		// See iec_60870_5_104_bug_report.md "Bug 3": the client swallows ASDU
		// decode errors (sf.Warn) unless this is enabled.
		settings.LogCfg = &client.LogCfg{Enable: true}
	}

	// With -demo the demo sequence is the only traffic source, so the gRPC
	// bridge is not built at all: masterCall.bridge stays nil and inbound
	// events are logged to stdout only (bridgeServer.publishEvent is nil-safe).
	var bridge *bridgeServer
	if !*demo {
		bridge = newBridgeServer(nil) // client wired in below, once c exists
	}
	c := client.New(settings, &masterCall{bridge: bridge})
	if bridge != nil {
		bridge.client = c
	}
	c.SetOnConnectHandler(func(_ *client.Client) {
		fmt.Println("[client] connected to outstation")
	})

	// The master traffic starts once the outstation has confirmed STARTDT.
	c.SetServerActiveHandler(func(c *client.Client) {
		fmt.Println("[client] outstation active (STARTDT confirmed)")
		if *demo {
			fmt.Println("[client] -demo set - sending demo commands")
			sendDemoCommands(c, uint16(*ca))
		}
	})

	// The gRPC bridge is an optional adapter: with -demo nothing drives it and
	// nothing subscribes to it, so we skip it entirely.
	var grpcServer *grpc.Server
	if !*demo {
		var err error
		grpcServer, err = startGRPCServer(*grpcSocket, bridge)
		if err != nil {
			log.Fatalf("grpc server: %v", err)
		}
	} else {
		log.Println("[client] -demo set: gRPC bridge disabled, running demo sequence only")
	}

	if err := c.Connect(); err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	log.Printf("connected to %s:%d (common addr %d)", *host, *port, *ca)

	ctx, cancel := context.WithCancel(context.Background())
	if *duration > 0 {
		go func() {
			time.Sleep(*duration)
			cancel()
		}()
	} else {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sig
			cancel()
		}()
	}
	<-ctx.Done()

	if grpcServer != nil {
		grpcServer.GracefulStop()
	}
	if err := c.Close(); err != nil {
		log.Printf("close: %v", err)
	}
	log.Println("client stopped")
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
			fmt.Printf("[client] ERROR sending %s: %v\n", cmd.name, err)
			continue
		}
		fmt.Printf("[client] %s sent\n", cmd.name)
	}
}
