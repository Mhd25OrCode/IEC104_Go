package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/wendy512/go-iecp5/asdu"
)

func TestTypeIDFromName(t *testing.T) {
	cases := []struct {
		in   string
		want asdu.TypeID
		ok   bool
	}{
		// Canonical bridge protocol form: the bare mnemonic.
		{"C_SC_NA_1", asdu.C_SC_NA_1, true},
		{"M_SP_NA_1", asdu.M_SP_NA_1, true},
		{"C_IC_NA_1", asdu.C_IC_NA_1, true},
		// The MSL5 "<mnemonic>_<typeID>" form (type ids per standard 101).
		{"C_SC_NA_1_45", asdu.C_SC_NA_1, true},
		{"C_SE_NC_1_50", asdu.C_SE_NC_1, true},
		{"C_IC_NA_1_100", asdu.C_IC_NA_1, true},
		{"M_SP_NA_1_1", asdu.M_SP_NA_1, true},
		// Unresolvable: must not silently guess.
		{"C_SC_NA", 0, false},
		{"X_FOO_1", 0, false},
		{"X_FOO_1_99", 0, false},
		{"C_SC_NA_1_99_99", 0, false},
		{"C_SC_NA_1_", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := typeIDFromName(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("typeIDFromName(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestCommandValueFromPayload pins the IEC104_GO_BRIDGE_SYNC.md contract: the
// backend sends a correctly typed `payload.value` for every command point, and
// a missing/null value is a protocol violation that must be reported instead of
// being defaulted to false/0 by cast.ToBoolE(nil) and friends.
func TestCommandValueFromPayload(t *testing.T) {
	cases := []struct {
		name    string
		typeId  asdu.TypeID
		payload string
		want    any
		wantErr bool
	}{
		// Typed scalars, one per command family in the sync doc's table.
		{"single bool true", asdu.C_SC_NA_1, `{"value":true}`, true, false},
		{"single bool false (tagged)", asdu.C_SC_TA_1, `{"value":false}`, false, false},
		{"double uint8", asdu.C_DC_NA_1, `{"value":2}`, uint8(2), false},
		{"regulating uint8 (tagged)", asdu.C_RC_TA_1, `{"value":255}`, uint8(255), false},
		{"setpoint scaled int16", asdu.C_SE_NB_1, `{"value":-1234}`, int16(-1234), false},
		{"setpoint short int16 (tagged)", asdu.C_SE_TA_1, `{"value":32767}`, int16(32767), false},
		{"setpoint float32", asdu.C_SE_NC_1, `{"value":125.5}`, float32(125.5), false},
		{"bitstring uint32 (tagged)", asdu.C_BO_TA_1, `{"value":4294967295}`, uint32(4294967295), false},
		// Rejected: no value, a null value, or a value that is not a scalar.
		{"no payload at all", asdu.C_SC_NA_1, ``, nil, true},
		{"empty object", asdu.C_SC_NA_1, `{}`, nil, true},
		{"explicit null value", asdu.C_SC_NA_1, `{"value":null}`, nil, true},
		// The old round-trip bug shape: a decoded inbound ASDU must not be cast.
		{"inbound ASDU echoed as value", asdu.C_SC_NA_1,
			`{"value":{"Ioa":100,"Qoc":{},"Time":"2026","Value":false},"cot":3}`, nil, true},
		{"malformed payload", asdu.C_SC_NA_1, `{"value":`, nil, true},
	}
	for _, c := range cases {
		got, err := commandValueFromPayload(c.typeId, json.RawMessage(c.payload))
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error, got %#v", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v (%T), want %#v (%T)", c.name, got, got, c.want, c.want)
		}
	}
}
