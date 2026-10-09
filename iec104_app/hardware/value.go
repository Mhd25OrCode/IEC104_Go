package hardware

import (
	"fmt"

	"github.com/spf13/cast"
	"github.com/wendy512/go-iecp5/asdu"
)

// CommandValue converts a loosely typed value (e.g. a JSON number or a string)
// into the Go type client.Client.SendCmd expects for typeId, mirroring the type
// switch in wendy512/iec104's doSend.
//
// A nil value is rejected: cast.ToBoolE(nil) and friends return the zero value
// with a nil error, which would send a bogus command.
func CommandValue(typeId asdu.TypeID, value any) (any, error) {
	if value == nil {
		return nil, fmt.Errorf("missing value for command %s", typeNameFromID(typeId))
	}
	switch typeId {
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		return cast.ToBoolE(value)
	case asdu.C_DC_NA_1, asdu.C_DC_TA_1, asdu.C_RC_NA_1, asdu.C_RC_TA_1:
		return cast.ToUint8E(value)
	case asdu.C_SE_NA_1, asdu.C_SE_TA_1, asdu.C_SE_NB_1, asdu.C_SE_TB_1:
		return cast.ToInt16E(value)
	case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		return cast.ToFloat32E(value)
	case asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		return cast.ToUint32E(value)
	default:
		return nil, fmt.Errorf("command %s does not take a value", typeNameFromID(typeId))
	}
}

// TypeID resolves a mnemonic such as "C_SC_NA_1" (optionally suffixed with its
// numeric type id, "C_SC_NA_1_45") to an asdu.TypeID.
func TypeID(name string) (asdu.TypeID, bool) { return typeIDFromName(name) }

// TypeName returns the mnemonic of an asdu.TypeID.
func TypeName(typeId asdu.TypeID) string { return typeNameFromID(typeId) }
