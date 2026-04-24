package ul

import (
	"errors"
	"reflect"
)

type PDUType byte

const (
	PDUAssociateRQ PDUType = 0x01
	PDUAssociateAC PDUType = 0x02
	PDUAssociateRJ PDUType = 0x03
	PDUDataTF      PDUType = 0x04
	PDUReleaseRQ   PDUType = 0x05
	PDUReleaseRP   PDUType = 0x06
	PDUAbort       PDUType = 0x07
)

const (
	PDUHeaderSize  uint32 = 6
	PDVHeaderSize  uint32 = 6
	DefaultMaxPDU  uint32 = 16_384 - PDUHeaderSize
	MinimumPDUSize uint32 = 1_024 - PDUHeaderSize
)

const (
	ApplicationContextName           = "1.2.840.10008.3.1.1.1"
	ImplementationClassUID           = "1.2.826.0.1.3680043.10.543.1"
	ImplementationVersionName        = "DICOMGO"
	MaxAETitleLength                 = 16
	DefaultProtocolVersion    uint16 = 1
)

const (
	ImplicitVRLittleEndian = "1.2.840.10008.1.2"
	ExplicitVRLittleEndian = "1.2.840.10008.1.2.1"
)

const (
	ItemApplicationContext    byte = 0x10
	ItemPresentationContextRQ byte = 0x20
	ItemPresentationContextAC byte = 0x21
	ItemAbstractSyntax        byte = 0x30
	ItemTransferSyntax        byte = 0x40
	ItemUserInformation       byte = 0x50
)

const (
	SubItemMaxLength                   byte = 0x51
	SubItemImplementationClassUID      byte = 0x52
	SubItemImplementationVersionName   byte = 0x55
	SubItemSopClassExtendedNegotiation byte = 0x56
	SubItemUserIdentity                byte = 0x58
)

const (
	AssociateRJResultPermanent byte = 1
	AssociateRJResultTransient byte = 2
)

const (
	AssociateRJSourceServiceUser                 byte = 1
	AssociateRJSourceServiceProviderACSE         byte = 2
	AssociateRJSourceServiceProviderPresentation byte = 3
)

const (
	AssociateRJReasonNoReasonGiven                      byte = 1
	AssociateRJReasonApplicationContextNameNotSupported byte = 2
	AssociateRJReasonProtocolVersionNotSupported        byte = 2
	AssociateRJReasonCallingAETitleNotRecognized        byte = 3
	AssociateRJReasonCalledAETitleNotRecognized         byte = 7
)

const (
	PresentationContextAcceptance                   byte = 0
	PresentationContextUserRejection                byte = 1
	PresentationContextNoReason                     byte = 2
	PresentationContextAbstractSyntaxNotSupported   byte = 3
	PresentationContextTransferSyntaxesNotSupported byte = 4
)

const (
	AbortSourceServiceUser     byte = 0
	AbortSourceReserved        byte = 1
	AbortSourceServiceProvider byte = 2
)

const (
	AbortReasonNotSpecified             byte = 0
	AbortReasonUnrecognizedPDU          byte = 1
	AbortReasonUnexpectedPDU            byte = 2
	AbortReasonReserved                 byte = 3
	AbortReasonUnrecognizedPDUParameter byte = 4
	AbortReasonUnexpectedPDUParameter   byte = 5
	AbortReasonInvalidPDUParameter      byte = 6
)

const (
	UserIdentityUsername         byte = 1
	UserIdentityUsernamePassword byte = 2
	UserIdentityKerberos         byte = 3
	UserIdentitySAML             byte = 4
	UserIdentityJWT              byte = 5
)

var (
	ErrInvalidPDU      = errors.New("dicom ul: invalid PDU")
	ErrPDUTooLarge     = errors.New("dicom ul: PDU too large")
	ErrInvalidPDUSize  = errors.New("dicom ul: invalid maximum PDU size")
	ErrLengthOverflow  = errors.New("dicom ul: length overflow")
	ErrUnsupportedPDU  = errors.New("dicom ul: unsupported PDU")
	ErrInvalidPDUField = errors.New("dicom ul: invalid PDU field")
	ErrMissingPDUField = errors.New("dicom ul: missing PDU field")
	ErrInvalidPDUItem  = errors.New("dicom ul: invalid PDU item")
	ErrInvalidUserItem = errors.New("dicom ul: invalid user information item")
	ErrInvalidAEtitle  = errors.New("dicom ul: invalid AE title")
	ErrInvalidPCID     = errors.New("dicom ul: invalid presentation context ID")
)

type PDU interface {
	pduType() PDUType
}

type UnknownPDU struct {
	Type PDUType
	Data []byte
}

type AssociationRQ struct {
	ProtocolVersion        uint16
	CalledAETitle          string
	CallingAETitle         string
	ApplicationContextName string
	PresentationContexts   []PresentationContextProposed
	UserInfo               []UserVariableItem
}

type AssociationAC struct {
	ProtocolVersion        uint16
	CalledAETitle          string
	CallingAETitle         string
	ApplicationContextName string
	PresentationContexts   []PresentationContextResult
	UserInfo               []UserVariableItem
}

type AssociationRJ struct {
	Result byte
	Source byte
	Reason byte
}

type PDataTF struct {
	Values []PDataValue
}

type PDataValue struct {
	PresentationContextID byte
	IsCommand             bool
	IsLast                bool
	Data                  []byte
}

type ReleaseRQ struct{}
type ReleaseRP struct{}

type AbortRQ struct {
	Source byte
	Reason byte
}

type PresentationContextProposed struct {
	ID                 byte
	AbstractSyntaxUID  string
	TransferSyntaxUIDs []string
}

// PresentationContext is kept as a compatibility alias for association options.
type PresentationContext = PresentationContextProposed

type PresentationContextResult struct {
	ID                byte
	Result            byte
	TransferSyntaxUID string
}

type UserVariableItem interface {
	userVariableItem()
}

type MaxLengthItem struct {
	Value uint32
}

type ImplementationClassUIDItem struct {
	UID string
}

type ImplementationVersionNameItem struct {
	Name string
}

type SopClassExtendedNegotiationItem struct {
	SopClassUID string
	Data        []byte
}

type UserIdentityItem struct {
	Type                      byte
	PositiveResponseRequested bool
	PrimaryField              []byte
	SecondaryField            []byte
}

type UnknownUserItem struct {
	Type byte
	Data []byte
}

func (p UnknownPDU) pduType() PDUType                     { return p.Type }
func (AssociationRQ) pduType() PDUType                    { return PDUAssociateRQ }
func (AssociationAC) pduType() PDUType                    { return PDUAssociateAC }
func (AssociationRJ) pduType() PDUType                    { return PDUAssociateRJ }
func (PDataTF) pduType() PDUType                          { return PDUDataTF }
func (ReleaseRQ) pduType() PDUType                        { return PDUReleaseRQ }
func (ReleaseRP) pduType() PDUType                        { return PDUReleaseRP }
func (AbortRQ) pduType() PDUType                          { return PDUAbort }
func (MaxLengthItem) userVariableItem()                   {}
func (ImplementationClassUIDItem) userVariableItem()      {}
func (ImplementationVersionNameItem) userVariableItem()   {}
func (SopClassExtendedNegotiationItem) userVariableItem() {}
func (UserIdentityItem) userVariableItem()                {}
func (UnknownUserItem) userVariableItem()                 {}

func PDUTypeOf(pdu PDU) PDUType {
	if pdu == nil {
		return 0
	}
	v := reflect.ValueOf(pdu)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return 0
		}
	}
	return pdu.pduType()
}

func validPresentationContextID(id byte) bool {
	return id != 0 && id%2 == 1
}

func validReject(result, source, reason byte) bool {
	if result != AssociateRJResultPermanent && result != AssociateRJResultTransient {
		return false
	}
	switch source {
	case AssociateRJSourceServiceUser:
		return reason >= 1 && reason <= 10
	case AssociateRJSourceServiceProviderACSE:
		return reason == 1 || reason == 2
	case AssociateRJSourceServiceProviderPresentation:
		return reason <= 7
	default:
		return false
	}
}

func validAbort(source, reason byte) bool {
	switch source {
	case AbortSourceServiceUser:
		return true
	case AbortSourceServiceProvider:
		return reason <= AbortReasonInvalidPDUParameter
	default:
		return false
	}
}
