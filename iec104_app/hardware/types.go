package hardware

import (
	"strings"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

// Command is one master-to-outstation request. Value is only used by the
// setpoint/switching/bitstring command types (see CommandValue); Meta is an
// opaque caller tag echoed back on later Events for the same address.
type Command struct {
	Type  asdu.TypeID
	CA    asdu.CommonAddr
	IOA   asdu.InfoObjAddr
	Value any
	Meta  any
}

// Direction tells whether a point is an input (monitoring) or output (command).
type Direction string

const (
	DirectionIn  Direction = "IN"
	DirectionOut Direction = "OUT"
)

// Event is one decoded outstation message.
type Event struct {
	CA        asdu.CommonAddr
	IOA       asdu.InfoObjAddr
	Type      asdu.TypeID
	TypeName  string
	Direction Direction
	COT       asdu.CauseOfTransmission
	Value     any
	// Meta is the tag of the last Command sent to the same (CA, IOA), or nil.
	Meta any
	Time time.Time
}

// directionOf classifies command types (C_*) as outputs and everything else as
// inputs.
func directionOf(typeName string) Direction {
	if strings.HasPrefix(typeName, "C_") {
		return DirectionOut
	}
	return DirectionIn
}
