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
)

func main() {
	host := flag.String("host", "127.0.0.1", "server host")
	port := flag.Int("port", 2404, "server port")
	ca := flag.Uint("ca", 1, "ASDU common address")
	duration := flag.Duration("duration", 30*time.Second, "how long to run (0 = until Ctrl+C)")
	flag.Parse()

	settings := client.NewSettings()
	settings.Host = *host
	settings.Port = *port
	settings.AutoConnect = true

	c := client.New(settings, &masterCall{})
	c.SetOnConnectHandler(func(_ *client.Client) {
		fmt.Println("[client] connected to outstation")
	})

	// The master traffic starts once the outstation has confirmed STARTDT.
	c.SetServerActiveHandler(func(c *client.Client) {
		fmt.Println("[client] outstation active (STARTDT confirmed) - sending demo commands")
		sendDemoCommands(c, uint16(*ca))
	})

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
	}
	for _, cmd := range cmds {
		if err := cmd.fn(); err != nil {
			fmt.Printf("[client] ERROR sending %s: %v\n", cmd.name, err)
			continue
		}
		fmt.Printf("[client] %s sent\n", cmd.name)
	}
}