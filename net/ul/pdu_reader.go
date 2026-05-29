package ul

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
)

const maxAssociationPDU = 16 << 20

func ReadPDU(r io.Reader, maxPDULength uint32) (PDU, error) {
	if maxPDULength < MinimumPDUSize {
		return nil, fmt.Errorf("%w: max PDU length %d is smaller than %d", ErrInvalidPDUSize, maxPDULength, MinimumPDUSize)
	}

	var header [PDUHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("dicom ul: read PDU header: %w", err)
	}

	pduType := PDUType(header[0])
	pduLength, err := dicomenc.NewBasicDecoder(binary.BigEndian).ReadU32(bytes.NewReader(header[2:]))
	if err != nil {
		return nil, fmt.Errorf("dicom ul: read PDU length: %w", err)
	}
	if err := validateReadPDULength(pduType, pduLength, maxPDULength); err != nil {
		return nil, err
	}

	data := make([]byte, pduLength)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("dicom ul: read PDU body: %w", err)
	}

	switch pduType {
	case PDUAssociateRQ:
		return readAssociationRQ(data)
	case PDUAssociateAC:
		return readAssociationAC(data)
	case PDUAssociateRJ:
		return readAssociationRJ(data)
	case PDUDataTF:
		return readPDataTF(data)
	case PDUReleaseRQ:
		return readReleaseRQ(data)
	case PDUReleaseRP:
		return readReleaseRP(data)
	case PDUAbort:
		return readAbortRQ(data)
	default:
		return &UnknownPDU{Type: pduType, Data: append([]byte(nil), data...)}, nil
	}
}

func validateReadPDULength(pduType PDUType, pduLength, maxPDULength uint32) error {
	limit := maxPDULength
	if pduType == PDUAssociateRQ || pduType == PDUAssociateAC {
		limit = maxAssociationPDU
	}
	if pduLength > limit {
		return fmt.Errorf("%w: length %d exceeds max %d", ErrPDUTooLarge, pduLength, limit)
	}
	return nil
}

func readAssociationRQ(data []byte) (*AssociationRQ, error) {
	protocolVersion, called, calling, variableItems, err := readAssociationFixed(data)
	if err != nil {
		return nil, err
	}
	appContext, presContexts, userVars, err := readVariableItems(variableItems, false)
	if err != nil {
		return nil, err
	}
	contexts, ok := presContexts.([]PresentationContextProposed)
	if !ok {
		return nil, fmt.Errorf("%w: presentation context request item type", ErrInvalidPDUItem)
	}
	return &AssociationRQ{
		ProtocolVersion:        protocolVersion,
		CalledAETitle:          called,
		CallingAETitle:         calling,
		ApplicationContextName: appContext,
		PresentationContexts:   contexts,
		UserInfo:               userVars,
	}, nil
}

func readAssociationAC(data []byte) (*AssociationAC, error) {
	protocolVersion, called, calling, variableItems, err := readAssociationFixed(data)
	if err != nil {
		return nil, err
	}
	appContext, presContexts, userVars, err := readVariableItems(variableItems, true)
	if err != nil {
		return nil, err
	}
	contexts, ok := presContexts.([]PresentationContextResult)
	if !ok {
		return nil, fmt.Errorf("%w: presentation context accept item type", ErrInvalidPDUItem)
	}
	return &AssociationAC{
		ProtocolVersion:        protocolVersion,
		CalledAETitle:          called,
		CallingAETitle:         calling,
		ApplicationContextName: appContext,
		PresentationContexts:   contexts,
		UserInfo:               userVars,
	}, nil
}

func readAssociationFixed(data []byte) (protocolVersion uint16, calledAETitle string, callingAETitle string, variableItems []byte, err error) {
	if len(data) < 68 {
		return 0, "", "", nil, fmt.Errorf("%w: association fixed fields need 68 bytes, got %d", ErrInvalidPDUField, len(data))
	}
	dec := dicomenc.NewBasicDecoder(binary.BigEndian)
	r := bytes.NewReader(data)
	protocolVersion, err = dec.ReadU16(r)
	if err != nil {
		return 0, "", "", nil, err
	}
	if _, err = dec.ReadU16(r); err != nil {
		return 0, "", "", nil, err
	}
	var calledBytes, callingBytes [16]byte
	if _, err = io.ReadFull(r, calledBytes[:]); err != nil {
		return 0, "", "", nil, err
	}
	if _, err = io.ReadFull(r, callingBytes[:]); err != nil {
		return 0, "", "", nil, err
	}
	if _, err = r.Seek(32, io.SeekCurrent); err != nil {
		return 0, "", "", nil, err
	}
	return protocolVersion, strings.TrimSpace(string(calledBytes[:])), strings.TrimSpace(string(callingBytes[:])), data[68:], nil
}

func readAssociationRJ(data []byte) (*AssociationRJ, error) {
	if len(data) != 4 {
		return nil, fmt.Errorf("%w: A-ASSOCIATE-RJ length %d", ErrInvalidPDUField, len(data))
	}
	pdu := &AssociationRJ{Result: data[1], Source: data[2], Reason: data[3]}
	if !validReject(pdu.Result, pdu.Source, pdu.Reason) {
		return nil, fmt.Errorf("%w: invalid A-ASSOCIATE-RJ result/source/reason %d/%d/%d", ErrInvalidPDUField, pdu.Result, pdu.Source, pdu.Reason)
	}
	return pdu, nil
}

func readVariableItems(data []byte, forAC bool) (appContext string, presContexts interface{}, userVars []UserVariableItem, err error) {
	var proposed []PresentationContextProposed
	var results []PresentationContextResult
	var haveAppContext bool
	var haveUserInformation bool
	r := bytes.NewReader(data)

	for r.Len() > 0 {
		itemType, item, err := readItem(r)
		if err != nil {
			return "", nil, nil, err
		}
		switch itemType {
		case ItemApplicationContext:
			if haveAppContext {
				return "", nil, nil, fmt.Errorf("%w: duplicate application-context", ErrInvalidPDUItem)
			}
			appContext = readApplicationContext(item)
			if appContext == "" {
				return "", nil, nil, fmt.Errorf("%w: empty application-context", ErrInvalidPDUItem)
			}
			haveAppContext = true
		case ItemPresentationContextRQ:
			if forAC {
				return "", nil, nil, fmt.Errorf("%w: request presentation context in A-ASSOCIATE-AC", ErrInvalidPDUItem)
			}
			pc, err := readPresentationContextProposed(item)
			if err != nil {
				return "", nil, nil, err
			}
			proposed = append(proposed, *pc)
		case ItemPresentationContextAC:
			if !forAC {
				return "", nil, nil, fmt.Errorf("%w: result presentation context in A-ASSOCIATE-RQ", ErrInvalidPDUItem)
			}
			pc, err := readPresentationContextResult(item)
			if err != nil {
				return "", nil, nil, err
			}
			results = append(results, *pc)
		case ItemUserInformation:
			if haveUserInformation {
				return "", nil, nil, fmt.Errorf("%w: duplicate user-information", ErrInvalidPDUItem)
			}
			userVars, err = readUserInformation(item)
			if err != nil {
				return "", nil, nil, err
			}
			if len(userVars) == 0 {
				return "", nil, nil, fmt.Errorf("%w: empty user-information", ErrInvalidPDUItem)
			}
			haveUserInformation = true
		default:
			continue
		}
	}
	if !haveAppContext {
		return "", nil, nil, fmt.Errorf("%w: application context name", ErrMissingPDUField)
	}
	if forAC {
		if err := validatePresentationContextIDsResult(results); err != nil {
			return "", nil, nil, err
		}
		return appContext, results, userVars, nil
	}
	if err := validatePresentationContextIDsProposed(proposed); err != nil {
		return "", nil, nil, err
	}
	return appContext, proposed, userVars, nil
}

func readApplicationContext(data []byte) string {
	return strings.TrimSpace(string(data))
}

func readPresentationContextProposed(data []byte) (*PresentationContextProposed, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: presentation context request fixed fields", ErrInvalidPDUItem)
	}
	pc := &PresentationContextProposed{ID: data[0]}
	if !validPresentationContextID(pc.ID) {
		return nil, fmt.Errorf("%w: %d", ErrInvalidPCID, pc.ID)
	}
	r := bytes.NewReader(data[4:])
	for r.Len() > 0 {
		itemType, item, err := readItem(r)
		if err != nil {
			return nil, err
		}
		switch itemType {
		case ItemAbstractSyntax:
			if pc.AbstractSyntaxUID != "" {
				return nil, fmt.Errorf("%w: multiple abstract syntax sub-items", ErrInvalidPDUItem)
			}
			pc.AbstractSyntaxUID = strings.TrimSpace(string(item))
		case ItemTransferSyntax:
			pc.TransferSyntaxUIDs = append(pc.TransferSyntaxUIDs, strings.TrimSpace(string(item)))
		default:
			continue
		}
	}
	if pc.AbstractSyntaxUID == "" {
		return nil, fmt.Errorf("%w: abstract syntax", ErrMissingPDUField)
	}
	if len(pc.TransferSyntaxUIDs) == 0 {
		return nil, fmt.Errorf("%w: transfer syntax", ErrMissingPDUField)
	}
	return pc, nil
}

func readPresentationContextResult(data []byte) (*PresentationContextResult, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: presentation context accept fixed fields", ErrInvalidPDUItem)
	}
	pc := &PresentationContextResult{ID: data[0], Result: data[2]}
	if !validPresentationContextID(pc.ID) {
		return nil, fmt.Errorf("%w: %d", ErrInvalidPCID, pc.ID)
	}
	if pc.Result > PresentationContextTransferSyntaxesNotSupported {
		return nil, fmt.Errorf("%w: presentation context result %d", ErrInvalidPDUField, pc.Result)
	}
	r := bytes.NewReader(data[4:])
	for r.Len() > 0 {
		itemType, item, err := readItem(r)
		if err != nil {
			return nil, err
		}
		switch itemType {
		case ItemTransferSyntax:
			if pc.TransferSyntaxUID != "" {
				return nil, fmt.Errorf("%w: multiple accepted transfer syntaxes", ErrInvalidPDUItem)
			}
			pc.TransferSyntaxUID = strings.TrimSpace(string(item))
		default:
			continue
		}
	}
	if pc.Result == PresentationContextAcceptance && pc.TransferSyntaxUID == "" {
		return nil, fmt.Errorf("%w: transfer syntax", ErrMissingPDUField)
	}
	return pc, nil
}

func readUserInformation(data []byte) ([]UserVariableItem, error) {
	r := bytes.NewReader(data)
	dec := dicomenc.NewBasicDecoder(binary.BigEndian)
	var items []UserVariableItem
	asyncSeen := false
	for r.Len() > 0 {
		itemType, item, err := readItem(r)
		if err != nil {
			return nil, err
		}
		switch itemType {
		case SubItemMaxLength:
			if len(item) != 4 {
				return nil, fmt.Errorf("%w: maximum length item length %d", ErrInvalidUserItem, len(item))
			}
			value, err := dec.ReadU32(bytes.NewReader(item))
			if err != nil {
				return nil, err
			}
			items = append(items, MaxLengthItem{Value: value})
		case SubItemImplementationClassUID:
			items = append(items, ImplementationClassUIDItem{UID: strings.TrimSpace(string(item))})
		case SubItemImplementationVersionName:
			items = append(items, ImplementationVersionNameItem{Name: strings.TrimSpace(string(item))})
		case SubItemAsynchronousOperations:
			if asyncSeen {
				return nil, fmt.Errorf("%w: duplicate asynchronous operations window", ErrInvalidUserItem)
			}
			asyncSeen = true
			if len(item) != 4 {
				return nil, fmt.Errorf("%w: asynchronous operations window item length %d", ErrInvalidUserItem, len(item))
			}
			invoked, err := dec.ReadU16(bytes.NewReader(item[:2]))
			if err != nil {
				return nil, err
			}
			performed, err := dec.ReadU16(bytes.NewReader(item[2:]))
			if err != nil {
				return nil, err
			}
			items = append(items, AsynchronousOperationsWindow{MaximumInvoked: invoked, MaximumPerformed: performed})
		case SubItemSopClassExtendedNegotiation:
			if len(item) < 2 {
				return nil, fmt.Errorf("%w: SOP class extended negotiation length %d", ErrInvalidUserItem, len(item))
			}
			sopLen, err := dec.ReadU16(bytes.NewReader(item[:2]))
			if err != nil {
				return nil, err
			}
			if int(sopLen) > len(item)-2 {
				return nil, fmt.Errorf("%w: SOP class UID length %d exceeds item length %d", ErrInvalidUserItem, sopLen, len(item))
			}
			items = append(items, SopClassExtendedNegotiationItem{
				SopClassUID: strings.TrimSpace(string(item[2 : 2+int(sopLen)])),
				Data:        append([]byte(nil), item[2+int(sopLen):]...),
			})
		case SubItemRoleSelection:
			role, err := readRoleSelection(item)
			if err != nil {
				return nil, err
			}
			items = append(items, role)
		case SubItemUserIdentity:
			userIdentity, err := readUserIdentity(item)
			if err != nil {
				return nil, err
			}
			items = append(items, userIdentity)
		case SubItemUserIdentityResponse:
			userIdentityResponse, err := readUserIdentityResponse(item)
			if err != nil {
				return nil, err
			}
			items = append(items, userIdentityResponse)
		default:
			items = append(items, UnknownUserItem{Type: itemType, Data: append([]byte(nil), item...)})
		}
	}
	return items, nil
}

func readRoleSelection(data []byte) (RoleSelectionItem, error) {
	if len(data) < 4 {
		return RoleSelectionItem{}, fmt.Errorf("%w: role selection item length %d", ErrInvalidUserItem, len(data))
	}
	dec := dicomenc.NewBasicDecoder(binary.BigEndian)
	sopLen, err := dec.ReadU16(bytes.NewReader(data[:2]))
	if err != nil {
		return RoleSelectionItem{}, err
	}
	if int(sopLen) > len(data)-4 {
		return RoleSelectionItem{}, fmt.Errorf("%w: role selection SOP class UID length %d exceeds item length %d", ErrInvalidUserItem, sopLen, len(data))
	}
	if len(data) != 2+int(sopLen)+2 {
		return RoleSelectionItem{}, fmt.Errorf("%w: role selection item length %d does not match SOP class UID length %d", ErrInvalidUserItem, len(data), sopLen)
	}
	scuRole := data[2+int(sopLen)]
	scpRole := data[2+int(sopLen)+1]
	if scuRole > 1 || scpRole > 1 {
		return RoleSelectionItem{}, fmt.Errorf("%w: role selection role values %d/%d", ErrInvalidUserItem, scuRole, scpRole)
	}
	return RoleSelectionItem{
		SopClassUID: strings.TrimSpace(string(data[2 : 2+int(sopLen)])),
		SCURole:     scuRole == 1,
		SCPRole:     scpRole == 1,
	}, nil
}

func readUserIdentity(data []byte) (UserIdentityItem, error) {
	if len(data) < 6 {
		return UserIdentityItem{}, fmt.Errorf("%w: user identity item length %d", ErrInvalidUserItem, len(data))
	}
	dec := dicomenc.NewBasicDecoder(binary.BigEndian)
	primaryLen, err := dec.ReadU16(bytes.NewReader(data[2:4]))
	if err != nil {
		return UserIdentityItem{}, err
	}
	pos := 4
	if int(primaryLen) > len(data)-pos-2 {
		return UserIdentityItem{}, fmt.Errorf("%w: primary field length %d exceeds item length %d", ErrInvalidUserItem, primaryLen, len(data))
	}
	primary := append([]byte(nil), data[pos:pos+int(primaryLen)]...)
	pos += int(primaryLen)
	secondaryLen, err := dec.ReadU16(bytes.NewReader(data[pos : pos+2]))
	if err != nil {
		return UserIdentityItem{}, err
	}
	pos += 2
	if int(secondaryLen) != len(data)-pos {
		return UserIdentityItem{}, fmt.Errorf("%w: secondary field length %d does not match remaining %d", ErrInvalidUserItem, secondaryLen, len(data)-pos)
	}
	return UserIdentityItem{
		Type:                      data[0],
		PositiveResponseRequested: data[1] != 0,
		PrimaryField:              primary,
		SecondaryField:            append([]byte(nil), data[pos:]...),
	}, nil
}

func readUserIdentityResponse(data []byte) (UserIdentityResponseItem, error) {
	if len(data) < 2 {
		return UserIdentityResponseItem{}, fmt.Errorf("%w: user identity response item length %d", ErrInvalidUserItem, len(data))
	}
	responseLen, err := dicomenc.NewBasicDecoder(binary.BigEndian).ReadU16(bytes.NewReader(data[:2]))
	if err != nil {
		return UserIdentityResponseItem{}, err
	}
	if int(responseLen) != len(data)-2 {
		return UserIdentityResponseItem{}, fmt.Errorf("%w: server response length %d does not match remaining %d", ErrInvalidUserItem, responseLen, len(data)-2)
	}
	return UserIdentityResponseItem{ServerResponse: append([]byte(nil), data[2:]...)}, nil
}

func readPDataTF(data []byte) (*PDataTF, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: P-DATA-TF must contain at least one P-DATA value", ErrInvalidPDUField)
	}
	r := bytes.NewReader(data)
	dec := dicomenc.NewBasicDecoder(binary.BigEndian)
	var values []PDataValue
	for r.Len() > 0 {
		if r.Len() < 6 {
			return nil, fmt.Errorf("%w: P-DATA value needs at least 6 bytes, got %d", ErrInvalidPDUField, r.Len())
		}
		itemLength, err := dec.ReadU32(r)
		if err != nil {
			return nil, err
		}
		if itemLength < 2 {
			return nil, fmt.Errorf("%w: P-DATA value item length %d", ErrInvalidPDUField, itemLength)
		}
		if itemLength > uint32(r.Len()) {
			return nil, fmt.Errorf("%w: P-DATA value item length %d exceeds remaining %d", ErrInvalidPDUField, itemLength, r.Len())
		}
		pcID, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if !validPresentationContextID(pcID) {
			return nil, fmt.Errorf("%w: %d", ErrInvalidPCID, pcID)
		}
		flags, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		valueData := make([]byte, itemLength-2)
		if _, err := io.ReadFull(r, valueData); err != nil {
			return nil, err
		}
		values = append(values, PDataValue{
			PresentationContextID: pcID,
			IsCommand:             flags&0x01 != 0,
			IsLast:                flags&0x02 != 0,
			Data:                  valueData,
		})
	}
	return &PDataTF{Values: values}, nil
}

func readReleaseRQ(data []byte) (*ReleaseRQ, error) {
	if len(data) != 4 {
		return nil, fmt.Errorf("%w: A-RELEASE-RQ length %d", ErrInvalidPDUField, len(data))
	}
	return &ReleaseRQ{}, nil
}

func readReleaseRP(data []byte) (*ReleaseRP, error) {
	if len(data) != 4 {
		return nil, fmt.Errorf("%w: A-RELEASE-RP length %d", ErrInvalidPDUField, len(data))
	}
	return &ReleaseRP{}, nil
}

func readAbortRQ(data []byte) (*AbortRQ, error) {
	if len(data) != 4 {
		return nil, fmt.Errorf("%w: A-ABORT length %d", ErrInvalidPDUField, len(data))
	}
	pdu := &AbortRQ{Source: data[2], Reason: data[3]}
	if !validAbort(pdu.Source, pdu.Reason) {
		return nil, fmt.Errorf("%w: invalid A-ABORT source/reason %d/%d", ErrInvalidPDUField, pdu.Source, pdu.Reason)
	}
	return pdu, nil
}

func readItem(r *bytes.Reader) (byte, []byte, error) {
	if r.Len() < 4 {
		return 0, nil, fmt.Errorf("%w: item header needs 4 bytes, got %d", ErrInvalidPDUItem, r.Len())
	}
	itemType, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	if _, err := r.ReadByte(); err != nil {
		return 0, nil, err
	}
	length, err := dicomenc.NewBasicDecoder(binary.BigEndian).ReadU16(r)
	if err != nil {
		return 0, nil, err
	}
	if int(length) > r.Len() {
		return 0, nil, fmt.Errorf("%w: item type 0x%02x length %d exceeds remaining %d", ErrInvalidPDUItem, itemType, length, r.Len())
	}
	item := make([]byte, length)
	if _, err := io.ReadFull(r, item); err != nil {
		return 0, nil, err
	}
	return itemType, item, nil
}
