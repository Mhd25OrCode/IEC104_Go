package main

import (
	"github.com/wendy512/go-iecp5/asdu"

	"github.com/alfas/iec104-app/hardware"
)

// demoCommands exercises every supported master-to-station command.
func demoCommands(ca asdu.CommonAddr) []hardware.Command {
	return []hardware.Command{
		{Type: asdu.C_IC_NA_1, CA: ca},
		{Type: asdu.C_CI_NA_1, CA: ca},
		{Type: asdu.C_RD_NA_1, CA: ca, IOA: 100},
		{Type: asdu.C_CS_NA_1, CA: ca},
		{Type: asdu.C_TS_TA_1, CA: ca},
		{Type: asdu.C_RP_NA_1, CA: ca},
		{Type: asdu.C_SC_NA_1, CA: ca, IOA: 1000, Value: true},
		{Type: asdu.C_DC_NA_1, CA: ca, IOA: 2000, Value: uint8(asdu.DPIDeterminedOn)},
		{Type: asdu.C_SE_NC_1, CA: ca, IOA: 3000, Value: float32(125.5)},
		{Type: asdu.C_BO_TA_1, CA: ca, IOA: 4000, Value: uint32(0x00FF00FF)},
		// C_BO_NA_1 is the untagged twin of C_BO_TA_1 and the one that survives
		// the peer's decoder: go-iecp5's infoObjSize table has no entry for
		// C_BO_TA_1, so the peer drops that frame - see
		// iec_60870_5_104_bug_report.md "Bug 3".
		{Type: asdu.C_BO_NA_1, CA: ca, IOA: 4001, Value: uint32(0x0F0F0F0F)},
	}
}
