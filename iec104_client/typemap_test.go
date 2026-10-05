package main

import (
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
