package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wendy512/go-iecp5/asdu"

	"github.com/alfas/iec104-app/hardware"
)

func main() {
	host := flag.String("host", "127.0.0.1", "server host")
	port := flag.Int("port", 2404, "server port")
	ca := flag.Uint("ca", 1, "ASDU common address")
	duration := flag.Duration("duration", 0, "how long to run (0 = until Ctrl+C/SIGTERM)")
	verbose := flag.Bool("verbose", false, "enable go-iecp5 frame/decode-error logging")
	demo := flag.Bool("demo", false, "send the built-in demo command sequence once the outstation is active")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	os.Exit(run(log, *host, *port, asdu.CommonAddr(*ca), *duration, *verbose, *demo))
}

func run(log *slog.Logger, host string, port int, ca asdu.CommonAddr, duration time.Duration, verbose, demo bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}

	var hw *hardware.Layer
	hw = hardware.New(hardware.Config{
		Host:    host,
		Port:    port,
		Verbose: verbose,
		Logger:  log,
		OnActive: func() {
			if !demo {
				return
			}
			log.Info("sending demo commands")
			if err := hw.SendAll(demoCommands(ca)); err != nil {
				log.Error("demo commands", "err", err)
			}
		},
	})

	go consumeEvents(ctx, hw, log)

	if err := hw.Run(ctx); err != nil {
		log.Error("hardware layer", "err", err)
		return 1
	}
	log.Info("client stopped")
	return 0
}
