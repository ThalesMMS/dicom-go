package ul

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
)

func WritePDU(w io.Writer, pdu PDU) error {
	if pdu == nil {
		return fmt.Errorf("%w: nil PDU", ErrInvalidPDU)
	}

	var body bytes.Buffer
	pduType, err := writePDUBody(&body, pdu)
	if err != nil {
		return err
	}
	if body.Len() > math.MaxUint32 {
		return fmt.Errorf("%w: PDU body length %d exceeds uint32", ErrLengthOverflow, body.Len())
	}

	var header bytes.Buffer
	header.WriteByte(byte(pduType))
	header.WriteByte(0)
	if err := dicomenc.NewBasicEncoder(binary.BigEndian).WriteU32(&header, uint32(body.Len())); err != nil {
		return fmt.Errorf("dicom ul: write PDU length: %w", err)
	}
	if err := writeAll(w, header.Bytes()); err != nil {
		return fmt.Errorf("dicom ul: write PDU header: %w", err)
	}
	if err := writeAll(w, body.Bytes()); err != nil {
		return fmt.Errorf("dicom ul: write PDU body: %w", err)
	}
	return nil
}

func writePDUBody(buf *bytes.Buffer, pdu PDU) (PDUType, error) {
	switch p := pdu.(type) {
	case AssociationRQ:
		return PDUAssociateRQ, writeAssociationRQ(buf, &p)
	case *AssociationRQ:
		if p == nil {
			return 0, fmt.Errorf("%w: nil *AssociationRQ", ErrInvalidPDU)
		}
		return PDUAssociateRQ, writeAssociationRQ(buf, p)
	case AssociationAC:
		return PDUAssociateAC, writeAssociationAC(buf, &p)
	case *AssociationAC:
		if p == nil {
			return 0, fmt.Errorf("%w: nil *AssociationAC", ErrInvalidPDU)
		}
		return PDUAssociateAC, writeAssociationAC(buf, p)
	case AssociationRJ:
		return PDUAssociateRJ, writeAssociationRJ(buf, &p)
	case *AssociationRJ:
		if p == nil {
			return 0, fmt.Errorf("%w: nil *AssociationRJ", ErrInvalidPDU)
		}
		return PDUAssociateRJ, writeAssociationRJ(buf, p)
	case PDataTF:
		return PDUDataTF, writePDataTF(buf, &p)
	case *PDataTF:
		if p == nil {
			return 0, fmt.Errorf("%w: nil *PDataTF", ErrInvalidPDU)
		}
		return PDUDataTF, writePDataTF(buf, p)
	case ReleaseRQ:
		return PDUReleaseRQ, writeReleaseRQ(buf)
	case *ReleaseRQ:
		if p == nil {
			return 0, fmt.Errorf("%w: nil *ReleaseRQ", ErrInvalidPDU)
		}
		return PDUReleaseRQ, writeReleaseRQ(buf)
	case ReleaseRP:
		return PDUReleaseRP, writeReleaseRP(buf)
	case *ReleaseRP:
		if p == nil {
			return 0, fmt.Errorf("%w: nil *ReleaseRP", ErrInvalidPDU)
		}
		return PDUReleaseRP, writeReleaseRP(buf)
	case AbortRQ:
		return PDUAbort, writeAbortRQ(buf, &p)
	case *AbortRQ:
		if p == nil {
			return 0, fmt.Errorf("%w: nil *AbortRQ", ErrInvalidPDU)
		}
		return PDUAbort, writeAbortRQ(buf, p)
	case UnknownPDU:
		buf.Write(p.Data)
		return p.Type, nil
	case *UnknownPDU:
		if p == nil {
			return 0, fmt.Errorf("%w: nil *UnknownPDU", ErrInvalidPDU)
		}
		buf.Write(p.Data)
		return p.Type, nil
	default:
		return 0, fmt.Errorf("%w: %T", ErrUnsupportedPDU, pdu)
	}
}

func writeAssociationRQ(buf *bytes.Buffer, rq *AssociationRQ) error {
	if rq == nil {
		return fmt.Errorf("%w: nil *AssociationRQ", ErrInvalidPDU)
	}
	if len(rq.PresentationContexts) == 0 {
		return fmt.Errorf("%w: A-ASSOCIATE-RQ requires at least one presentation context", ErrInvalidPDU)
	}
	if err := validatePresentationContextIDsProposed(rq.PresentationContexts); err != nil {
		return err
	}
	if rq.UserInfo == nil {
		return fmt.Errorf("%w: A-ASSOCIATE-RQ requires a User Information item", ErrInvalidPDU)
	}
	if err := writeAssociationFixed(buf, rq.ProtocolVersion, rq.CalledAETitle, rq.CallingAETitle); err != nil {
		return err
	}
	if err := writeApplicationContext(buf, rq.ApplicationContextName); err != nil {
		return err
	}
	for i := range rq.PresentationContexts {
		if err := writePresentationContextProposed(buf, &rq.PresentationContexts[i]); err != nil {
			return err
		}
	}
	return writeUserInformation(buf, rq.UserInfo)
}

func writeAssociationAC(buf *bytes.Buffer, ac *AssociationAC) error {
	if ac == nil {
		return fmt.Errorf("%w: nil *AssociationAC", ErrInvalidPDU)
	}
	if len(ac.PresentationContexts) == 0 {
		return fmt.Errorf("%w: A-ASSOCIATE-AC requires at least one presentation context", ErrInvalidPDU)
	}
	if err := validatePresentationContextIDsResult(ac.PresentationContexts); err != nil {
		return err
	}
	if ac.UserInfo == nil {
		return fmt.Errorf("%w: A-ASSOCIATE-AC requires a User Information item", ErrInvalidPDU)
	}
	if err := writeAssociationFixed(buf, ac.ProtocolVersion, ac.CalledAETitle, ac.CallingAETitle); err != nil {
		return err
	}
	if err := writeApplicationContext(buf, ac.ApplicationContextName); err != nil {
		return err
	}
	for i := range ac.PresentationContexts {
		if err := writePresentationContextResult(buf, &ac.PresentationContexts[i]); err != nil {
			return err
		}
	}
	return writeUserInformation(buf, ac.UserInfo)
}

func validatePresentationContextIDsProposed(contexts []PresentationContextProposed) error {
	if len(contexts) > MaxPresentationContexts {
		return fmt.Errorf("%w: presentation context count %d exceeds %d", ErrInvalidPCID, len(contexts), MaxPresentationContexts)
	}
	seen := make(map[byte]bool, len(contexts))
	for _, context := range contexts {
		if !validPresentationContextID(context.ID) || seen[context.ID] {
			return fmt.Errorf("%w: duplicate or invalid presentation context ID %d", ErrInvalidPCID, context.ID)
		}
		seen[context.ID] = true
	}
	return nil
}

func validatePresentationContextIDsResult(contexts []PresentationContextResult) error {
	if len(contexts) > MaxPresentationContexts {
		return fmt.Errorf("%w: presentation context count %d exceeds %d", ErrInvalidPCID, len(contexts), MaxPresentationContexts)
	}
	seen := make(map[byte]bool, len(contexts))
	for _, context := range contexts {
		if !validPresentationContextID(context.ID) || seen[context.ID] {
			return fmt.Errorf("%w: duplicate or invalid presentation context ID %d", ErrInvalidPCID, context.ID)
		}
		seen[context.ID] = true
	}
	return nil
}

func writeAssociationFixed(buf *bytes.Buffer, protocolVersion uint16, calledAETitle, callingAETitle string) error {
	enc := dicomenc.NewBasicEncoder(binary.BigEndian)
	if err := enc.WriteU16(buf, protocolVersion); err != nil {
		return err
	}
	if err := enc.WriteU16(buf, 0); err != nil {
		return err
	}
	if err := writeAETitle(buf, calledAETitle); err != nil {
		return fmt.Errorf("called AE title: %w", err)
	}
	if err := writeAETitle(buf, callingAETitle); err != nil {
		return fmt.Errorf("calling AE title: %w", err)
	}
	buf.Write(make([]byte, 32))
	return nil
}

func writeAssociationRJ(buf *bytes.Buffer, rj *AssociationRJ) error {
	if rj == nil {
		return fmt.Errorf("%w: nil *AssociationRJ", ErrInvalidPDU)
	}
	if !validReject(rj.Result, rj.Source, rj.Reason) {
		return fmt.Errorf("%w: invalid A-ASSOCIATE-RJ result/source/reason %d/%d/%d", ErrInvalidPDUField, rj.Result, rj.Source, rj.Reason)
	}
	buf.WriteByte(0)
	buf.WriteByte(rj.Result)
	buf.WriteByte(rj.Source)
	buf.WriteByte(rj.Reason)
	return nil
}

func writeApplicationContext(buf *bytes.Buffer, name string) error {
	if name == "" {
		return fmt.Errorf("%w: application context name", ErrMissingPDUField)
	}
	return writeItem(buf, ItemApplicationContext, []byte(name))
}

func writePresentationContextProposed(buf *bytes.Buffer, pc *PresentationContextProposed) error {
	if pc == nil {
		return fmt.Errorf("%w: nil *PresentationContextProposed", ErrInvalidPDU)
	}
	if !validPresentationContextID(pc.ID) {
		return fmt.Errorf("%w: %d", ErrInvalidPCID, pc.ID)
	}
	if pc.AbstractSyntaxUID == "" {
		return fmt.Errorf("%w: abstract syntax", ErrMissingPDUField)
	}
	if len(pc.TransferSyntaxUIDs) == 0 {
		return fmt.Errorf("%w: transfer syntax", ErrMissingPDUField)
	}

	var item bytes.Buffer
	item.Write([]byte{pc.ID, 0, 0, 0})
	if err := writeItem(&item, ItemAbstractSyntax, []byte(pc.AbstractSyntaxUID)); err != nil {
		return err
	}
	for _, transferSyntax := range pc.TransferSyntaxUIDs {
		if transferSyntax == "" {
			return fmt.Errorf("%w: transfer syntax", ErrMissingPDUField)
		}
		if err := writeItem(&item, ItemTransferSyntax, []byte(transferSyntax)); err != nil {
			return err
		}
	}
	return writeItem(buf, ItemPresentationContextRQ, item.Bytes())
}

func writePresentationContextResult(buf *bytes.Buffer, pc *PresentationContextResult) error {
	if pc == nil {
		return fmt.Errorf("%w: nil *PresentationContextResult", ErrInvalidPDU)
	}
	if !validPresentationContextID(pc.ID) {
		return fmt.Errorf("%w: %d", ErrInvalidPCID, pc.ID)
	}
	if pc.Result > PresentationContextTransferSyntaxesNotSupported {
		return fmt.Errorf("%w: presentation context result %d", ErrInvalidPDUField, pc.Result)
	}
	if pc.TransferSyntaxUID == "" {
		return fmt.Errorf("%w: transfer syntax", ErrMissingPDUField)
	}

	var item bytes.Buffer
	item.Write([]byte{pc.ID, 0, pc.Result, 0})
	if err := writeItem(&item, ItemTransferSyntax, []byte(pc.TransferSyntaxUID)); err != nil {
		return err
	}
	return writeItem(buf, ItemPresentationContextAC, item.Bytes())
}

func writeUserInformation(buf *bytes.Buffer, items []UserVariableItem) error {
	var item bytes.Buffer
	asyncSeen := false
	for _, userItem := range items {
		if _, ok := asAsynchronousOperationsWindow(userItem); ok {
			if asyncSeen {
				return fmt.Errorf("%w: duplicate asynchronous operations window", ErrInvalidUserItem)
			}
			asyncSeen = true
		}
		if err := writeUserVariableItem(&item, userItem); err != nil {
			return err
		}
	}
	return writeItem(buf, ItemUserInformation, item.Bytes())
}

func writeUserVariableItem(buf *bytes.Buffer, item UserVariableItem) error {
	switch it := item.(type) {
	case MaxLengthItem:
		return writeMaxLengthItem(buf, &it)
	case *MaxLengthItem:
		return writeMaxLengthItem(buf, it)
	case ImplementationClassUIDItem:
		return writeItem(buf, SubItemImplementationClassUID, []byte(it.UID))
	case *ImplementationClassUIDItem:
		if it == nil {
			return fmt.Errorf("%w: nil *ImplementationClassUIDItem", ErrInvalidUserItem)
		}
		return writeItem(buf, SubItemImplementationClassUID, []byte(it.UID))
	case ImplementationVersionNameItem:
		return writeImplementationVersionNameItem(buf, &it)
	case *ImplementationVersionNameItem:
		return writeImplementationVersionNameItem(buf, it)
	case AsynchronousOperationsWindow:
		return writeAsynchronousOperationsWindow(buf, &it)
	case *AsynchronousOperationsWindow:
		return writeAsynchronousOperationsWindow(buf, it)
	case SopClassExtendedNegotiationItem:
		return writeSopClassExtendedNegotiationItem(buf, &it)
	case *SopClassExtendedNegotiationItem:
		return writeSopClassExtendedNegotiationItem(buf, it)
	case RoleSelectionItem:
		return writeRoleSelectionItem(buf, &it)
	case *RoleSelectionItem:
		return writeRoleSelectionItem(buf, it)
	case UserIdentityItem:
		return writeUserIdentityItem(buf, &it)
	case *UserIdentityItem:
		return writeUserIdentityItem(buf, it)
	case UserIdentityResponseItem:
		return writeUserIdentityResponseItem(buf, &it)
	case *UserIdentityResponseItem:
		return writeUserIdentityResponseItem(buf, it)
	case UnknownUserItem:
		return writeItem(buf, it.Type, it.Data)
	case *UnknownUserItem:
		if it == nil {
			return fmt.Errorf("%w: nil *UnknownUserItem", ErrInvalidUserItem)
		}
		return writeItem(buf, it.Type, it.Data)
	default:
		return fmt.Errorf("%w: %T", ErrInvalidUserItem, item)
	}
}

func writeAsynchronousOperationsWindow(buf *bytes.Buffer, item *AsynchronousOperationsWindow) error {
	if item == nil {
		return fmt.Errorf("%w: nil *AsynchronousOperationsWindow", ErrInvalidUserItem)
	}
	var data bytes.Buffer
	encoder := dicomenc.NewBasicEncoder(binary.BigEndian)
	if err := encoder.WriteU16(&data, item.MaximumInvoked); err != nil {
		return err
	}
	if err := encoder.WriteU16(&data, item.MaximumPerformed); err != nil {
		return err
	}
	return writeItem(buf, SubItemAsynchronousOperations, data.Bytes())
}

func writeMaxLengthItem(buf *bytes.Buffer, item *MaxLengthItem) error {
	if item == nil {
		return fmt.Errorf("%w: nil *MaxLengthItem", ErrInvalidUserItem)
	}
	var data bytes.Buffer
	if err := dicomenc.NewBasicEncoder(binary.BigEndian).WriteU32(&data, item.Value); err != nil {
		return err
	}
	return writeItem(buf, SubItemMaxLength, data.Bytes())
}

func writeImplementationVersionNameItem(buf *bytes.Buffer, item *ImplementationVersionNameItem) error {
	if item == nil {
		return fmt.Errorf("%w: nil *ImplementationVersionNameItem", ErrInvalidUserItem)
	}
	if len(item.Name) > 16 {
		return fmt.Errorf("%w: implementation version name length %d exceeds 16", ErrInvalidUserItem, len(item.Name))
	}
	return writeItem(buf, SubItemImplementationVersionName, []byte(item.Name))
}

func writeSopClassExtendedNegotiationItem(buf *bytes.Buffer, item *SopClassExtendedNegotiationItem) error {
	if item == nil {
		return fmt.Errorf("%w: nil *SopClassExtendedNegotiationItem", ErrInvalidUserItem)
	}
	if len(item.SopClassUID) > math.MaxUint16 {
		return fmt.Errorf("%w: SOP class UID length %d exceeds uint16", ErrLengthOverflow, len(item.SopClassUID))
	}

	var data bytes.Buffer
	if err := dicomenc.NewBasicEncoder(binary.BigEndian).WriteU16(&data, uint16(len(item.SopClassUID))); err != nil {
		return err
	}
	data.WriteString(item.SopClassUID)
	data.Write(item.Data)
	return writeItem(buf, SubItemSopClassExtendedNegotiation, data.Bytes())
}

func writeRoleSelectionItem(buf *bytes.Buffer, item *RoleSelectionItem) error {
	if item == nil {
		return fmt.Errorf("%w: nil *RoleSelectionItem", ErrInvalidUserItem)
	}
	if item.SopClassUID == "" {
		return fmt.Errorf("%w: SOP class UID", ErrMissingPDUField)
	}
	if len(item.SopClassUID) > math.MaxUint16 {
		return fmt.Errorf("%w: SOP class UID length %d exceeds uint16", ErrLengthOverflow, len(item.SopClassUID))
	}

	var data bytes.Buffer
	if err := dicomenc.NewBasicEncoder(binary.BigEndian).WriteU16(&data, uint16(len(item.SopClassUID))); err != nil {
		return err
	}
	data.WriteString(item.SopClassUID)
	if item.SCURole {
		data.WriteByte(1)
	} else {
		data.WriteByte(0)
	}
	if item.SCPRole {
		data.WriteByte(1)
	} else {
		data.WriteByte(0)
	}
	return writeItem(buf, SubItemRoleSelection, data.Bytes())
}

func writeUserIdentityItem(buf *bytes.Buffer, item *UserIdentityItem) error {
	if item == nil {
		return fmt.Errorf("%w: nil *UserIdentityItem", ErrInvalidUserItem)
	}

	var data bytes.Buffer
	data.WriteByte(item.Type)
	if item.PositiveResponseRequested {
		data.WriteByte(1)
	} else {
		data.WriteByte(0)
	}
	if err := writeU16LengthBytes(&data, item.PrimaryField); err != nil {
		return err
	}
	if err := writeU16LengthBytes(&data, item.SecondaryField); err != nil {
		return err
	}
	return writeItem(buf, SubItemUserIdentity, data.Bytes())
}

func writeUserIdentityResponseItem(buf *bytes.Buffer, item *UserIdentityResponseItem) error {
	if item == nil {
		return fmt.Errorf("%w: nil *UserIdentityResponseItem", ErrInvalidUserItem)
	}

	var data bytes.Buffer
	if err := writeU16LengthBytes(&data, item.ServerResponse); err != nil {
		return err
	}
	return writeItem(buf, SubItemUserIdentityResponse, data.Bytes())
}

func writePDataTF(buf *bytes.Buffer, pdata *PDataTF) error {
	if pdata == nil {
		return fmt.Errorf("%w: nil *PDataTF", ErrInvalidPDU)
	}
	if len(pdata.Values) == 0 {
		return fmt.Errorf("%w: P-DATA-TF without values", ErrMissingPDUField)
	}

	enc := dicomenc.NewBasicEncoder(binary.BigEndian)
	for _, value := range pdata.Values {
		if !validPresentationContextID(value.PresentationContextID) {
			return fmt.Errorf("%w: %d", ErrInvalidPCID, value.PresentationContextID)
		}
		if len(value.Data) > math.MaxUint32-2 {
			return fmt.Errorf("%w: P-DATA value length %d exceeds uint32", ErrLengthOverflow, len(value.Data))
		}
		if err := enc.WriteU32(buf, uint32(len(value.Data)+2)); err != nil {
			return err
		}
		buf.WriteByte(value.PresentationContextID)
		var flags byte
		if value.IsCommand {
			flags |= 0x01
		}
		if value.IsLast {
			flags |= 0x02
		}
		buf.WriteByte(flags)
		buf.Write(value.Data)
	}
	return nil
}

func writeReleaseRQ(buf *bytes.Buffer) error {
	buf.Write([]byte{0, 0, 0, 0})
	return nil
}

func writeReleaseRP(buf *bytes.Buffer) error {
	buf.Write([]byte{0, 0, 0, 0})
	return nil
}

func writeAbortRQ(buf *bytes.Buffer, abort *AbortRQ) error {
	if abort == nil {
		return fmt.Errorf("%w: nil *AbortRQ", ErrInvalidPDU)
	}
	if !validAbort(abort.Source, abort.Reason) {
		return fmt.Errorf("%w: invalid A-ABORT source/reason %d/%d", ErrInvalidPDUField, abort.Source, abort.Reason)
	}
	buf.Write([]byte{0, 0, abort.Source, abort.Reason})
	return nil
}

func writeItem(buf *bytes.Buffer, itemType byte, data []byte) error {
	if len(data) > math.MaxUint16 {
		return fmt.Errorf("%w: item 0x%02x length %d exceeds uint16", ErrLengthOverflow, itemType, len(data))
	}
	buf.WriteByte(itemType)
	buf.WriteByte(0)
	if err := dicomenc.NewBasicEncoder(binary.BigEndian).WriteU16(buf, uint16(len(data))); err != nil {
		return err
	}
	buf.Write(data)
	return nil
}

func writeU16LengthBytes(buf *bytes.Buffer, data []byte) error {
	if len(data) > math.MaxUint16 {
		return fmt.Errorf("%w: length %d exceeds uint16", ErrLengthOverflow, len(data))
	}
	if err := dicomenc.NewBasicEncoder(binary.BigEndian).WriteU16(buf, uint16(len(data))); err != nil {
		return err
	}
	buf.Write(data)
	return nil
}

func writeAETitle(buf *bytes.Buffer, title string) error {
	if len(title) > 16 {
		return fmt.Errorf("%w: length %d exceeds 16", ErrInvalidAEtitle, len(title))
	}
	buf.WriteString(title)
	for i := len(title); i < 16; i++ {
		buf.WriteByte(' ')
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
