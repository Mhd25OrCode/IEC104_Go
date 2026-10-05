package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cast"
	"github.com/wendy512/go-iecp5/asdu"
)

// typeIDByName converts the bridge protocol's point.asduTyp string (see
// IEC104_GO_BRIDGE.md) into the wendy512/go-iecp5 asdu.TypeID this client
// sends/decodes. Names match the companion standard 101 mnemonics
// exposed by the asdu package (verified via `go doc asdu.TypeID`).
var typeIDByName = map[string]asdu.TypeID{
	"M_SP_NA_1": asdu.M_SP_NA_1,
	"M_SP_TA_1": asdu.M_SP_TA_1,
	"M_DP_NA_1": asdu.M_DP_NA_1,
	"M_DP_TA_1": asdu.M_DP_TA_1,
	"M_ST_NA_1": asdu.M_ST_NA_1,
	"M_ST_TA_1": asdu.M_ST_TA_1,
	"M_BO_NA_1": asdu.M_BO_NA_1,
	"M_BO_TA_1": asdu.M_BO_TA_1,
	"M_ME_NA_1": asdu.M_ME_NA_1,
	"M_ME_TA_1": asdu.M_ME_TA_1,
	"M_ME_NB_1": asdu.M_ME_NB_1,
	"M_ME_TB_1": asdu.M_ME_TB_1,
	"M_ME_NC_1": asdu.M_ME_NC_1,
	"M_ME_TC_1": asdu.M_ME_TC_1,
	"M_IT_NA_1": asdu.M_IT_NA_1,
	"M_IT_TA_1": asdu.M_IT_TA_1,
	"M_EP_TA_1": asdu.M_EP_TA_1,
	"M_EP_TB_1": asdu.M_EP_TB_1,
	"M_EP_TC_1": asdu.M_EP_TC_1,
	"M_PS_NA_1": asdu.M_PS_NA_1,
	"M_ME_ND_1": asdu.M_ME_ND_1,
	"M_SP_TB_1": asdu.M_SP_TB_1,
	"M_DP_TB_1": asdu.M_DP_TB_1,
	"M_ST_TB_1": asdu.M_ST_TB_1,
	"M_BO_TB_1": asdu.M_BO_TB_1,
	"M_ME_TD_1": asdu.M_ME_TD_1,
	"M_ME_TE_1": asdu.M_ME_TE_1,
	"M_ME_TF_1": asdu.M_ME_TF_1,
	"M_IT_TB_1": asdu.M_IT_TB_1,
	"M_EP_TD_1": asdu.M_EP_TD_1,
	"M_EP_TE_1": asdu.M_EP_TE_1,
	"M_EP_TF_1": asdu.M_EP_TF_1,
	"M_EI_NA_1": asdu.M_EI_NA_1,
	"C_SC_NA_1": asdu.C_SC_NA_1,
	"C_DC_NA_1": asdu.C_DC_NA_1,
	"C_RC_NA_1": asdu.C_RC_NA_1,
	"C_SE_NA_1": asdu.C_SE_NA_1,
	"C_SE_NB_1": asdu.C_SE_NB_1,
	"C_SE_NC_1": asdu.C_SE_NC_1,
	"C_BO_NA_1": asdu.C_BO_NA_1,
	"C_SC_TA_1": asdu.C_SC_TA_1,
	"C_DC_TA_1": asdu.C_DC_TA_1,
	"C_RC_TA_1": asdu.C_RC_TA_1,
	"C_SE_TA_1": asdu.C_SE_TA_1,
	"C_SE_TB_1": asdu.C_SE_TB_1,
	"C_SE_TC_1": asdu.C_SE_TC_1,
	"C_BO_TA_1": asdu.C_BO_TA_1,
	"C_IC_NA_1": asdu.C_IC_NA_1,
	"C_CI_NA_1": asdu.C_CI_NA_1,
	"C_RD_NA_1": asdu.C_RD_NA_1,
	"C_CS_NA_1": asdu.C_CS_NA_1,
	"C_TS_NA_1": asdu.C_TS_NA_1,
	"C_RP_NA_1": asdu.C_RP_NA_1,
	"C_CD_NA_1": asdu.C_CD_NA_1,
	"C_TS_TA_1": asdu.C_TS_TA_1,
}

// typeIDFromName resolves a bridge point.asduTyp string to its asdu.TypeID.
//
// The canonical form is the bare mnemonic (IEC104_GO_BRIDGE.md), e.g.
// "C_SC_NA_1". Some MSL5 point records append the numeric type identification
// to the mnemonic - "C_SC_NA_1_45", where 45 is C_SC_NA_1's TypeID - so one
// trailing "_<digits>" suffix is stripped and the lookup retried. The suffix is
// only dropped once the remaining prefix is itself a known mnemonic, so the
// plain "C_SC_NA_1" (which legitimately ends in "_1") is never mangled.
func typeIDFromName(name string) (asdu.TypeID, bool) {
	if id, ok := typeIDByName[name]; ok {
		return id, true
	}
	if i := strings.LastIndexByte(name, '_'); i > 0 && isDigits(name[i+1:]) {
		if id, ok := typeIDByName[name[:i]]; ok {
			return id, true
		}
	}
	return 0, false
}

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// typeNameByID is the reverse of typeIDByName. asdu.TypeID.String() returns
// "TID<C_IC_NA_1>" (a debug format), not the bare mnemonic the bridge protocol
// expects, so outgoing events go through this table instead.
var typeNameByID = func() map[asdu.TypeID]string {
	m := make(map[asdu.TypeID]string, len(typeIDByName))
	for name, id := range typeIDByName {
		m[id] = name
	}
	return m
}()

// typeNameFromID resolves an asdu.TypeID to its bridge point.asduTyp string.
func typeNameFromID(typeId asdu.TypeID) string {
	if name, ok := typeNameByID[typeId]; ok {
		return name
	}
	return typeId.String()
}

// commandValueFromPayload decodes the `{"value": ...}` JSON object carried in
// point.payload into the Go type client.Client.SendCmd expects for
// typeId, mirroring the type switch in wendy512/iec104's doSend.
func commandValueFromPayload(typeId asdu.TypeID, payloadJSON json.RawMessage) (any, error) {
	var payload struct {
		Value any `json:"value"`
	}
	if len(payloadJSON) > 0 {
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
	}
	switch typeId {
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		return cast.ToBoolE(payload.Value)
	case asdu.C_DC_NA_1, asdu.C_DC_TA_1, asdu.C_RC_NA_1, asdu.C_RC_TA_1:
		return cast.ToUint8E(payload.Value)
	case asdu.C_SE_NA_1, asdu.C_SE_TA_1, asdu.C_SE_NB_1, asdu.C_SE_TB_1:
		return cast.ToInt16E(payload.Value)
	case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		return cast.ToFloat32E(payload.Value)
	case asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		return cast.ToUint32E(payload.Value)
	default:
		return nil, fmt.Errorf("asduTyp %v does not take a command value", typeId)
	}
}
