package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
)

func main() {
	msl5Host := flag.String("msl5-host", "127.0.0.1", "MSL5 IEC 104 server address to dial")
	msl5Port := flag.Int("msl5-port", 2404, "MSL5 IEC 104 server port")
	ca := flag.Uint("ca", 1, "Common Address of all outgoing commands")
	cmdInterval := flag.Duration("cmd-interval", 5*time.Second, "period of the command cycle (0 = only send-on-change)")
	cmdOnChange := flag.Bool("cmd-on-change", true, "send commands immediately (debounced) when a command value changes")
	reconnect := flag.Duration("reconnect", 2*time.Second, "delay between reconnect attempts")
	duration := flag.Duration("duration", 0, "run for this long, then exit (0 = until Ctrl+C)")
	verbose := flag.Bool("verbose", false, "enable wendy512/go-iecp5 library logging (frames + decode errors)")
	flag.Parse()

	if *ca == 0 || *ca > 0xFFFF {
		log.Fatalf("invalid -ca %d (want 1..65535)", *ca)
	}

	addr := net.JoinHostPort(*msl5Host, strconv.Itoa(*msl5Port))
	cli, snd, _, err := newClient(addr, asdu.CommonAddr(*ca), *cmdInterval, *cmdOnChange, *reconnect, *verbose)
	if err != nil {
		log.Fatal(err)
	}

	if err := cli.Start(); err != nil {
		log.Fatal(err)
	}
	log.Printf("IEC 104 client dialling MSL5 at %s (CA %d, reconnect %v)", addr, *ca, *reconnect)

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
	snd.Stop()
	_ = cli.Close()
	log.Println("client stopped")
}

// newClient wires the cs104 client, the monitoring image and the command
// sender. Start the returned client with Start(); it reconnects in the
// background.
func newClient(addr string, ca asdu.CommonAddr, interval time.Duration, onChange bool, reconnect time.Duration, verbose bool) (*cs104.Client, *sender, *station, error) {
	opt := cs104.NewOption()
	opt.SetReconnectInterval(reconnect)
	if err := opt.AddRemoteServer(addr); err != nil {
		return nil, nil, nil, err
	}

	st := newStation(verbose)
	cli := cs104.NewClient(&clientHandler{st: st, verbose: verbose}, opt)
	// Library logs are off by default and swallow ASDU decode errors.
	cli.LogMode(verbose)

	snd := newSender(cli, ca, defaultCommandPoints(), interval, onChange, verbose)

	// MSL5 never sends STARTDT; the client must. Sends are only possible once
	// STARTDT is confirmed, so the sender starts in the active handler.
	cli.SetOnConnectHandler(func(c *cs104.Client) {
		fmt.Printf("[client] connected to %s, sending STARTDT\n", addr)
		c.SendStartDt()
	})
	cli.SetServerActiveHandler(func(c *cs104.Client) {
		fmt.Println("[client] STARTDT confirmed, session active")
		snd.Start()
	})
	cli.SetConnectionLostHandler(func(c *cs104.Client) {
		fmt.Println("[client] connection lost")
		snd.Stop()
	})
	return cli, snd, st, nil
}

