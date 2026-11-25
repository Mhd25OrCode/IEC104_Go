package main

import (
	"github.com/wendy512/go-iecp5/asdu"
)

// coa wraps a cause into a CauseOfTransmission value.
//
// Outgoing frames are always built fresh rather than echoed through
// ASDU.SendReplyMirror(): go-iecp5's Get*Cmd() getters consume the internal
// info-object buffer, so a mirror built afterwards would carry a truncated
// information body (see docs/iec104_library_bug_report.md §1).
func coa(cause asdu.Cause) asdu.CauseOfTransmission {
	return asdu.CauseOfTransmission{Cause: cause}
}

// newCommandASDU builds a fresh single-object ASDU (SQ=0, Number=1).
func newCommandASDU(conn asdu.Connect, typeID asdu.TypeID, cause asdu.Cause, ca asdu.CommonAddr) *asdu.ASDU {
	return asdu.NewASDU(conn.Params(), asdu.Identifier{
		Type:       typeID,
		Variable:   asdu.VariableStruct{IsSequence: false, Number: 1},
		Coa:        coa(cause),
		CommonAddr: ca,
	})
}
