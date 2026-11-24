package main

import (
	"fmt"

	"github.com/wendy512/go-iecp5/asdu"
)

// clientHandler adapts the station to cs104.ClientHandlerInterface. Only
// control commands (ASDUHandler) are served; everything else is out of scope
// and is logged and never answered.
type clientHandler struct {
	st      *station
	verbose bool
}

func (h *clientHandler) ASDUHandler(c asdu.Connect, p *asdu.ASDU) error { return h.st.OnASDU(c, p) }

func (h *clientHandler) ignore(what string, p *asdu.ASDU) error {
	if h.verbose {
		fmt.Printf("[client] ignoring %s: type=%v COT=%v CA=%d\n", what, p.Type, p.Identifier.Coa, p.CommonAddr)
	}
	return nil
}

func (h *clientHandler) InterrogationHandler(_ asdu.Connect, p *asdu.ASDU) error {
	return h.ignore("interrogation", p)
}

func (h *clientHandler) CounterInterrogationHandler(_ asdu.Connect, p *asdu.ASDU) error {
	return h.ignore("counter interrogation", p)
}

func (h *clientHandler) ReadHandler(_ asdu.Connect, p *asdu.ASDU) error {
	return h.ignore("read", p)
}

func (h *clientHandler) TestCommandHandler(_ asdu.Connect, p *asdu.ASDU) error {
	return h.ignore("test command", p)
}

func (h *clientHandler) ClockSyncHandler(_ asdu.Connect, p *asdu.ASDU) error {
	return h.ignore("clock sync", p)
}

func (h *clientHandler) ResetProcessHandler(_ asdu.Connect, p *asdu.ASDU) error {
	return h.ignore("reset process", p)
}

func (h *clientHandler) DelayAcquisitionHandler(_ asdu.Connect, p *asdu.ASDU) error {
	return h.ignore("delay acquisition", p)
}
