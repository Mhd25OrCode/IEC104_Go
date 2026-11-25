package main

import (
	"fmt"

	"github.com/wendy512/go-iecp5/asdu"
)

// clientHandler adapts the station to cs104.ClientHandlerInterface. ASDUHandler
// receives MSL5's monitoring frames and confirmations; every request type is
// out of scope and is logged (with -log-frames) and never answered.
type clientHandler struct {
	st        *station
	logFrames bool
}

func (h *clientHandler) ASDUHandler(c asdu.Connect, p *asdu.ASDU) error { return h.st.OnASDU(c, p) }

func (h *clientHandler) ignore(what string, p *asdu.ASDU) error {
	if h.logFrames {
		fmt.Printf("[client] RX ignoring %s: type=%v %v CA=%d\n", what, p.Type, p.Identifier.Coa, p.CommonAddr)
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
