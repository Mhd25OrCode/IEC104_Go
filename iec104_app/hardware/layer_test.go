package hardware

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

func TestCommandValue(t *testing.T) {
	cases := []struct {
		name    string
		typeId  asdu.TypeID
		in      any
		want    any
		wantErr bool
	}{
		{"single bool", asdu.C_SC_NA_1, true, true, false},
		{"double uint8 from json number", asdu.C_DC_NA_1, float64(2), uint8(2), false},
		{"setpoint scaled int16", asdu.C_SE_NB_1, float64(-1234), int16(-1234), false},
		{"setpoint float32", asdu.C_SE_NC_1, 125.5, float32(125.5), false},
		{"bitstring uint32", asdu.C_BO_TA_1, float64(4294967295), uint32(4294967295), false},
		{"nil value rejected", asdu.C_SC_NA_1, nil, nil, true},
		{"non-value command rejected", asdu.C_IC_NA_1, true, nil, true},
	}
	for _, c := range cases {
		got, err := CommandValue(c.typeId, c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error, got %#v", c.name, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v, %v; want %#v", c.name, got, err, c.want)
		}
	}
}

type fakeClient struct {
	calls []string
	value any
}

func (f *fakeClient) SendCmd(_ uint16, _ asdu.TypeID, ioa asdu.InfoObjAddr, v any) error {
	f.calls = append(f.calls, "cmd")
	f.value = v
	return nil
}
func (f *fakeClient) SendInterrogationCmd(uint16) error { f.calls = append(f.calls, "ic"); return nil }
func (f *fakeClient) SendCounterInterrogationCmd(uint16) error {
	f.calls = append(f.calls, "ci")
	return nil
}
func (f *fakeClient) SendReadCmd(uint16, uint) error { f.calls = append(f.calls, "rd"); return nil }
func (f *fakeClient) SendClockSynchronizationCmd(uint16, time.Time) error {
	f.calls = append(f.calls, "cs")
	return nil
}
func (f *fakeClient) SendTestCmd(uint16) error { f.calls = append(f.calls, "ts"); return nil }
func (f *fakeClient) SendResetProcessCmd(uint16) error {
	f.calls = append(f.calls, "rp")
	return nil
}

func TestSendRequiresConnection(t *testing.T) {
	l := New(Config{})
	if err := l.Send(Command{Type: asdu.C_IC_NA_1}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("got %v, want ErrNotConnected", err)
	}
}

func TestSendAllCollectsFailures(t *testing.T) {
	l := New(Config{})
	f := &fakeClient{}
	var c commander = f
	l.cmd.Store(&c)

	err := l.SendAll([]Command{
		{Type: asdu.C_IC_NA_1, CA: 1},
		{Type: asdu.C_SC_NA_1, CA: 1, IOA: 5},             // missing value
		{Type: asdu.M_SP_NA_1, CA: 1},                     // not sendable
		{Type: asdu.C_SE_NC_1, CA: 1, IOA: 6, Value: 1.5}, // ok
	})
	if err == nil {
		t.Fatal("expected joined error")
	}
	if want := []string{"ic", "cmd"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	if f.value != float32(1.5) {
		t.Fatalf("value = %#v", f.value)
	}
}

func TestEventCarriesMetaAndDirection(t *testing.T) {
	l := New(Config{})
	var c commander = &fakeClient{}
	l.cmd.Store(&c)
	if err := l.Send(Command{Type: asdu.C_SC_NA_1, CA: 1, IOA: 7, Value: true, Meta: "row-7"}); err != nil {
		t.Fatal(err)
	}

	l.publish(1, 7, asdu.M_SP_NA_1, asdu.CauseOfTransmission{}, true)
	l.publish(1, 8, asdu.C_SC_NA_1, asdu.CauseOfTransmission{}, nil)
	a, b := <-l.Events(), <-l.Events()
	if a.Meta != "row-7" || a.Direction != DirectionIn || a.TypeName != "M_SP_NA_1" {
		t.Errorf("unexpected event %+v", a)
	}
	if b.Meta != nil || b.Direction != DirectionOut {
		t.Errorf("unexpected event %+v", b)
	}
}

func TestPublishNeverBlocksWhenBufferFull(t *testing.T) {
	l := New(Config{EventBuffer: 1})
	done := make(chan struct{})
	go func() {
		for range 5 {
			l.publish(1, 1, asdu.M_SP_NA_1, asdu.CauseOfTransmission{}, nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked")
	}
}
