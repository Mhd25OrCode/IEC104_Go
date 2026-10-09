package hardware

import (
	"fmt"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

// commander is the subset of *client.Client the layer drives; it exists so
// dispatching can be tested without an outstation.
type commander interface {
	SendCmd(addr uint16, typeId asdu.TypeID, ioa asdu.InfoObjAddr, value any) error
	SendInterrogationCmd(addr uint16) error
	SendCounterInterrogationCmd(addr uint16) error
	SendReadCmd(addr uint16, ioa uint) error
	SendClockSynchronizationCmd(addr uint16, t time.Time) error
	SendTestCmd(addr uint16) error
	SendResetProcessCmd(addr uint16) error
}

// dispatch maps a Command onto the matching client call, driven by its type.
func dispatch(c commander, cmd Command) error {
	ca := uint16(cmd.CA)
	switch cmd.Type {
	case asdu.C_IC_NA_1:
		return c.SendInterrogationCmd(ca)
	case asdu.C_CI_NA_1:
		return c.SendCounterInterrogationCmd(ca)
	case asdu.C_RD_NA_1:
		return c.SendReadCmd(ca, uint(cmd.IOA))
	case asdu.C_CS_NA_1:
		return c.SendClockSynchronizationCmd(ca, time.Now())
	case asdu.C_TS_NA_1, asdu.C_TS_TA_1:
		return c.SendTestCmd(ca)
	case asdu.C_RP_NA_1:
		return c.SendResetProcessCmd(ca)
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1, asdu.C_DC_NA_1, asdu.C_DC_TA_1,
		asdu.C_RC_NA_1, asdu.C_RC_TA_1, asdu.C_SE_NA_1, asdu.C_SE_TA_1,
		asdu.C_SE_NB_1, asdu.C_SE_TB_1, asdu.C_SE_NC_1, asdu.C_SE_TC_1,
		asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		value, err := CommandValue(cmd.Type, cmd.Value)
		if err != nil {
			return err
		}
		return c.SendCmd(ca, cmd.Type, cmd.IOA, value)
	default:
		return fmt.Errorf("type %s is not a sendable command", typeNameFromID(cmd.Type))
	}
}
