package hardware

import (
	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/iec104/client"
)

// masterCall implements client.ASDUCall — everything the master receives. Each
// callback runs on the library's handler goroutine and only forwards decoded
// values to emit, which never blocks.
type masterCall struct {
	emit func(ca asdu.CommonAddr, ioa asdu.InfoObjAddr, typeId asdu.TypeID, cot asdu.CauseOfTransmission, value any)
}

// OnInterrogation receives general-interrogation replies: ActivationCon, then
// data (separate M_* ASDUs handled by OnASDU), then ActivationTerm.
func (m *masterCall) OnInterrogation(p *asdu.ASDU) error {
	ioa, qoi := p.GetInterrogationCmd()
	m.emit(p.CommonAddr, ioa, p.Type, p.Coa, qoi)
	return nil
}

func (m *masterCall) OnCounterInterrogation(p *asdu.ASDU) error {
	ioa, qcc := p.GetCounterInterrogationCmd()
	m.emit(p.CommonAddr, ioa, p.Type, p.Coa, qcc)
	return nil
}

// OnRead only triggers if the peer echoes the read command itself; normally
// reads are answered with data ASDUs.
func (m *masterCall) OnRead(p *asdu.ASDU) error {
	m.emit(p.CommonAddr, p.GetReadCmd(), p.Type, p.Coa, nil)
	return nil
}

func (m *masterCall) OnTestCommand(p *asdu.ASDU) error {
	ioa, fbp := p.GetTestCommand()
	m.emit(p.CommonAddr, ioa, p.Type, p.Coa, fbp)
	return nil
}

func (m *masterCall) OnClockSync(p *asdu.ASDU) error {
	ioa, tm := p.GetClockSynchronizationCmd()
	m.emit(p.CommonAddr, ioa, p.Type, p.Coa, tm)
	return nil
}

func (m *masterCall) OnResetProcess(p *asdu.ASDU) error {
	ioa, qrp := p.GetResetProcessCmd()
	m.emit(p.CommonAddr, ioa, p.Type, p.Coa, qrp)
	return nil
}

func (m *masterCall) OnDelayAcquisition(p *asdu.ASDU) error {
	ioa, msec := p.GetDelayAcquireCommand()
	m.emit(p.CommonAddr, ioa, p.Type, p.Coa, msec)
	return nil
}

// OnASDU handles monitoring data and command confirmations that are not routed
// to a dedicated handler.
func (m *masterCall) OnASDU(p *asdu.ASDU) error {
	if m.onCommandReply(p) {
		return nil
	}
	switch client.GetDataType(p.Type) {
	case client.SinglePoint:
		for _, v := range p.GetSinglePoint() {
			m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
		}
	case client.DoublePoint:
		for _, v := range p.GetDoublePoint() {
			m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
		}
	case client.MeasuredValueNormal:
		for _, v := range p.GetMeasuredValueNormal() {
			m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
		}
	case client.MeasuredValueFloat:
		for _, v := range p.GetMeasuredValueFloat() {
			m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
		}
	case client.IntegratedTotals:
		for _, v := range p.GetIntegratedTotals() {
			m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
		}
	}
	return nil
}
