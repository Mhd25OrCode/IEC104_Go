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
	"github.com/wendy512/iec104/server"
)

func main() {
	host := flag.String("host", "0.0.0.0", "listen host")
	port := flag.Int("port", 2404, "listen port")
	ca := flag.Uint("ca", 1, "ASDU common address")
	period := flag.Duration("period", 3*time.Second, "periodic reporting interval")
	spont := flag.Duration("spontaneous", 2*time.Second, "spontaneous reporting interval")
	duration := flag.Duration("duration", 0, "run for this long, then exit (0 = until Ctrl+C)")
	verbose := flag.Bool("verbose", false, "enable wendy512/go-iecp5 library logging (frames + decode errors)")
	flag.Parse()

	settings := server.NewSettings()
	settings.Host = *host
	settings.Port = *port
	if *verbose {
		// Library logs are off by default: cs104 swallows ASDU decode errors
		// (e.g. the C_BO_TA_1 drop documented in iec_60870_5_104_bug_report.md
		// "Bug 3") behind sf.Error(), which prints nothing unless LogMode(true).
		settings.LogCfg = &server.LogCfg{Enable: true}
	}

	st := &station{
		ca:     asdu.CommonAddr(*ca),
		spVal:  true,
		dpVal:  asdu.DPIDeterminedOn,
		ana:    100,
		period: *period,
		spont:  *spont,
	}

	s := server.New(settings, st)
	s.SetOnConnectionHandler(func(conn asdu.Connect) {
		fmt.Printf("[server] client connected: %s\n", conn.UnderlyingConn().RemoteAddr())
	})
	s.SetConnectionLostHandler(func(conn asdu.Connect) {
		fmt.Printf("[server] client disconnected: %s\n", conn.UnderlyingConn().RemoteAddr())
	})

	s.Start()
	log.Printf("IEC 104 outstation listening on %s:%d (common addr %d)", *host, *port, *ca)

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
	s.Stop()
	log.Println("server stopped")
}