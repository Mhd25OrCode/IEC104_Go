package main

import (
	"context"
	"log/slog"

	"github.com/alfas/iec104-app/hardware"
)

// consumeEvents logs every hardware event until ctx is cancelled. It is the
// application's single event sink; replace it to route events elsewhere.
func consumeEvents(ctx context.Context, hw *hardware.Layer, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-hw.Events():
			log.Info("event",
				"type", ev.TypeName,
				"dir", ev.Direction,
				"ca", ev.CA,
				"ioa", ev.IOA,
				"cot", ev.COT.String(),
				"value", ev.Value,
			)
		}
	}
}
