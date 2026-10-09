package hardware

import "github.com/wendy512/go-iecp5/asdu"

// onCommandReply forwards control-command confirmations echoed by the server
// and reports whether the frame was one. (C_BO_NA_1 / C_BO_TA_1 confirmations
// carry a 32-bit bitstring value plus, for the TA variant, a CP56Time2a echo.)
func (m *masterCall) onCommandReply(p *asdu.ASDU) bool {
	switch p.Type {
	case asdu.C_TS_TA_1:
		ioa, fbp, _ := p.GetTestCommandCP56Time2a()
		m.emit(p.CommonAddr, ioa, p.Type, p.Coa, fbp)
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		v := p.GetSingleCmd()
		m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
	case asdu.C_DC_NA_1, asdu.C_DC_TA_1:
		v := p.GetDoubleCmd()
		m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
	case asdu.C_SE_NA_1, asdu.C_SE_TA_1:
		v := p.GetSetpointNormalCmd()
		m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
	case asdu.C_SE_NB_1, asdu.C_SE_TB_1:
		v := p.GetSetpointCmdScaled()
		m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
	case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		v := p.GetSetpointFloatCmd()
		m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
	case asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		v := p.GetBitsString32Cmd()
		m.emit(p.CommonAddr, v.Ioa, p.Type, p.Coa, v)
	default:
		return false
	}
	return true
}
