package main

import (
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

// coa wraps a cause into a CauseOfTransmission value.
//
// All confirmations below use a brand-new ASDU rather than echoing the
// received request through ASDU.SendReplyMirror(). That API is unsafe here:
// go-iecp5's Get*Cmd() getters consume the internal info-object buffer, so a
// mirrored ASDU built after parsing the request would carry an empty/truncated
// information body (see go-iecp5 codec.go Decode* methods).
func coa(cause asdu.Cause) asdu.CauseOfTransmission {
	return asdu.CauseOfTransmission{Cause: cause}
}

// newCommandASDU builds a fresh single-object ASDU (SQ=0, Number=1).
//
// It is named after its main use (command confirmations) but is deliberately
// generic: it is also the base for hand-built monitoring ASDUs, because the
// library's own monitoring helpers restrict the cause of transmission.
func newCommandASDU(conn asdu.Connect, typeID asdu.TypeID, cause asdu.Cause, ca asdu.CommonAddr) *asdu.ASDU {
	return asdu.NewASDU(conn.Params(), asdu.Identifier{
		Type:       typeID,
		Variable:   asdu.VariableStruct{IsSequence: false, Number: 1},
		Coa:        coa(cause),
		CommonAddr: ca,
	})
}

// sendMeasuredFloatTime reports a short-float measurement with a CP56Time2a
// time tag (M_ME_TF_1) as a brand-new ASDU.
//
// It cannot use asdu.MeasuredValueFloatCP56Time2a: that helper accepts only
// COT Spontaneous(3)/Request(5) and returns ErrCmdCause for Periodic(1) or
// InterrogatedByStation(20), both of which this outstation needs. The QDS mask
// (& 0xf1) mirrors the library's own measuredValueFloat() encoder.
func sendMeasuredFloatTime(conn asdu.Connect, cause asdu.Cause, ca asdu.CommonAddr, info asdu.MeasuredValueFloatInfo) error {
	u := newCommandASDU(conn, asdu.M_ME_TF_1, cause, ca)
	if err := u.AppendInfoObjAddr(info.Ioa); err != nil {
		return err
	}
	u.AppendFloat32(info.Value).AppendBytes(byte(info.Qds & 0xf1))
	u.AppendCP56Time2a(info.Time, conn.Params().InfoObjTimeZone)
	return conn.Send(u)
}

// confirmInterrogation sends a C_IC_NA_1 confirmation/termination
// (ActivationCon / ActivationTerm respectively).
func confirmInterrogation(conn asdu.Connect, cause asdu.Cause, ca asdu.CommonAddr, qoi asdu.QualifierOfInterrogation) error {
	u := newCommandASDU(conn, asdu.C_IC_NA_1, cause, ca)
	if err := u.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendBytes(byte(qoi))
	return conn.Send(u)
}

// confirmCounterInterrogation sends a C_CI_NA_1 confirmation/termination.
func confirmCounterInterrogation(conn asdu.Connect, cause asdu.Cause, ca asdu.CommonAddr, qcc asdu.QualifierCountCall) error {
	u := newCommandASDU(conn, asdu.C_CI_NA_1, cause, ca)
	if err := u.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendBytes(qcc.Value())
	return conn.Send(u)
}

// confirmClockSync sends a C_CS_NA_1 activation confirmation carrying the
// synchronization time received in the request.
func confirmClockSync(conn asdu.Connect, ca asdu.CommonAddr, t time.Time) error {
	u := newCommandASDU(conn, asdu.C_CS_NA_1, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendCP56Time2a(t, conn.Params().InfoObjTimeZone)
	return conn.Send(u)
}

// confirmResetProcess sends a C_RP_NA_1 activation confirmation.
func confirmResetProcess(conn asdu.Connect, ca asdu.CommonAddr, qrp asdu.QualifierOfResetProcessCmd) error {
	u := newCommandASDU(conn, asdu.C_RP_NA_1, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendBytes(byte(qrp))
	return conn.Send(u)
}

// confirmDelayAcquire sends a C_CD_NA_1 activation confirmation.
func confirmDelayAcquire(conn asdu.Connect, ca asdu.CommonAddr, msec uint16) error {
	u := newCommandASDU(conn, asdu.C_CD_NA_1, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendCP16Time2a(msec)
	return conn.Send(u)
}

// confirmTestCommand sends a C_TS_TA_1 activation confirmation with the
// standard test word and CP56Time2a timestamp.
func confirmTestCommand(conn asdu.Connect, ca asdu.CommonAddr, t time.Time) error {
	u := newCommandASDU(conn, asdu.C_TS_TA_1, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendUint16(asdu.FBPTestWord)
	u.AppendCP56Time2a(t, conn.Params().InfoObjTimeZone)
	return conn.Send(u)
}

// confirmSingleCmd echoes a single command as an activation confirmation.
func confirmSingleCmd(conn asdu.Connect, typeID asdu.TypeID, ca asdu.CommonAddr, cmd asdu.SingleCommandInfo) error {
	if typeID != asdu.C_SC_NA_1 && typeID != asdu.C_SC_TA_1 {
		return asdu.ErrTypeIDNotMatch
	}
	u := newCommandASDU(conn, typeID, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}
	v := cmd.Qoc.Value()
	if cmd.Value {
		v |= 0x01
	}
	u.AppendBytes(v)
	if typeID == asdu.C_SC_TA_1 {
		u.AppendCP56Time2a(cmd.Time, conn.Params().InfoObjTimeZone)
	}
	return conn.Send(u)
}

// confirmDoubleCmd echoes a double command as an activation confirmation.
func confirmDoubleCmd(conn asdu.Connect, typeID asdu.TypeID, ca asdu.CommonAddr, cmd asdu.DoubleCommandInfo) error {
	if typeID != asdu.C_DC_NA_1 && typeID != asdu.C_DC_TA_1 {
		return asdu.ErrTypeIDNotMatch
	}
	u := newCommandASDU(conn, typeID, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}
	u.AppendBytes(cmd.Qoc.Value() | byte(cmd.Value&0x03))
	if typeID == asdu.C_DC_TA_1 {
		u.AppendCP56Time2a(cmd.Time, conn.Params().InfoObjTimeZone)
	}
	return conn.Send(u)
}

// confirmSetpointNormal echoes a normalized set-point command as an
// activation confirmation.
func confirmSetpointNormal(conn asdu.Connect, typeID asdu.TypeID, ca asdu.CommonAddr, cmd asdu.SetpointCommandNormalInfo) error {
	if typeID != asdu.C_SE_NA_1 && typeID != asdu.C_SE_TA_1 {
		return asdu.ErrTypeIDNotMatch
	}
	u := newCommandASDU(conn, typeID, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}
	u.AppendNormalize(cmd.Value).AppendBytes(cmd.Qos.Value())
	if typeID == asdu.C_SE_TA_1 {
		u.AppendCP56Time2a(cmd.Time, conn.Params().InfoObjTimeZone)
	}
	return conn.Send(u)
}

// confirmSetpointScaled echoes a scaled set-point command as an activation
// confirmation.
func confirmSetpointScaled(conn asdu.Connect, typeID asdu.TypeID, ca asdu.CommonAddr, cmd asdu.SetpointCommandScaledInfo) error {
	if typeID != asdu.C_SE_NB_1 && typeID != asdu.C_SE_TB_1 {
		return asdu.ErrTypeIDNotMatch
	}
	u := newCommandASDU(conn, typeID, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}
	u.AppendScaled(cmd.Value).AppendBytes(cmd.Qos.Value())
	if typeID == asdu.C_SE_TB_1 {
		u.AppendCP56Time2a(cmd.Time, conn.Params().InfoObjTimeZone)
	}
	return conn.Send(u)
}

// confirmBitsString32Cmd echoes a 32-bit bitstring command (C_BO_NA_1) as an
// activation confirmation; the C_BO_TA_1 variant also echoes its CP56Time2a.
//
// Hand-built like every other confirmation here: asdu.BitsString32Cmd accepts
// only COT Activation(6)/Deactivation(8) and therefore cannot emit the
// ActivationCon(7) confirmation, and mirroring is unsafe after
// pack.GetBitsString32Cmd() has consumed the received ASDU.
func confirmBitsString32Cmd(conn asdu.Connect, typeID asdu.TypeID, ca asdu.CommonAddr, cmd asdu.BitsString32CommandInfo) error {
	if typeID != asdu.C_BO_NA_1 && typeID != asdu.C_BO_TA_1 {
		return asdu.ErrTypeIDNotMatch
	}
	u := newCommandASDU(conn, typeID, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}
	u.AppendBitsString32(cmd.Value)
	if typeID == asdu.C_BO_TA_1 {
		u.AppendCP56Time2a(cmd.Time, conn.Params().InfoObjTimeZone)
	}
	return conn.Send(u)
}

// confirmSetpointFloat echoes a short-float set-point command as an activation
// confirmation.
func confirmSetpointFloat(conn asdu.Connect, typeID asdu.TypeID, ca asdu.CommonAddr, cmd asdu.SetpointCommandFloatInfo) error {
	if typeID != asdu.C_SE_NC_1 && typeID != asdu.C_SE_TC_1 {
		return asdu.ErrTypeIDNotMatch
	}
	u := newCommandASDU(conn, typeID, asdu.ActivationCon, ca)
	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}
	u.AppendFloat32(cmd.Value).AppendBytes(cmd.Qos.Value())
	if typeID == asdu.C_SE_TC_1 {
		u.AppendCP56Time2a(cmd.Time, conn.Params().InfoObjTimeZone)
	}
	return conn.Send(u)
}