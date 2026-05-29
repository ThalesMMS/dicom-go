package ul

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

func TestAssociationRQRoundTrip(t *testing.T) {
	pdu := AssociationRQ{
		ProtocolVersion:        1,
		CalledAETitle:          "SCP",
		CallingAETitle:         "SCU",
		ApplicationContextName: "1.2.840.10008.3.1.1.1",
		PresentationContexts: []PresentationContextProposed{
			{
				ID:                 1,
				AbstractSyntaxUID:  "1.2.840.10008.1.1",
				TransferSyntaxUIDs: []string{"1.2.840.10008.1.2", "1.2.840.10008.1.2.1"},
			},
			{
				ID:                 3,
				AbstractSyntaxUID:  "1.2.840.10008.5.1.4.1.1.2",
				TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"},
			},
		},
		UserInfo: []UserVariableItem{
			ImplementationClassUIDItem{UID: "1.2.3.4"},
			ImplementationVersionNameItem{Name: "DICOM_GO"},
			MaxLengthItem{Value: DefaultMaxPDU},
			AsynchronousOperationsWindow{MaximumInvoked: 0, MaximumPerformed: 7},
			SopClassExtendedNegotiationItem{SopClassUID: "1.2.840.10008.1.1", Data: []byte{1, 0, 1, 1}},
			RoleSelectionItem{SopClassUID: "1.2.840.10008.1.1", SCURole: true, SCPRole: true},
			UserIdentityItem{
				Type:                      UserIdentityUsernamePassword,
				PositiveResponseRequested: true,
				PrimaryField:              []byte("user"),
				SecondaryField:            []byte("password"),
			},
			UnknownUserItem{Type: 0x99, Data: []byte{9, 8, 7}},
		},
	}

	var buf bytes.Buffer
	if err := WritePDU(&buf, pdu); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	got, err := ReadPDU(bytes.NewReader(buf.Bytes()), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	if !reflect.DeepEqual(got, &pdu) {
		t.Fatalf("round-trip mismatch:\ngot  %#v\nwant %#v", got, pdu)
	}
}

func TestAsynchronousOperationsWindowUserItemRoundTripPreservesZero(t *testing.T) {
	pdu := AssociationRQ{
		ProtocolVersion:        1,
		CalledAETitle:          "SCP",
		CallingAETitle:         "SCU",
		ApplicationContextName: ApplicationContextName,
		PresentationContexts: []PresentationContextProposed{{
			ID: 1, AbstractSyntaxUID: "1.2.840.10008.1.1", TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
		UserInfo: []UserVariableItem{AsynchronousOperationsWindow{MaximumInvoked: 0, MaximumPerformed: 9}},
	}
	var buf bytes.Buffer
	if err := WritePDU(&buf, pdu); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadPDU(bytes.NewReader(buf.Bytes()), DefaultMaxPDU)
	if err != nil {
		t.Fatal(err)
	}
	rq := decoded.(*AssociationRQ)
	if len(rq.UserInfo) != 1 {
		t.Fatalf("UserInfo length = %d, want 1", len(rq.UserInfo))
	}
	window, ok := rq.UserInfo[0].(AsynchronousOperationsWindow)
	if !ok || window.MaximumInvoked != 0 || window.MaximumPerformed != 9 {
		t.Fatalf("window = %#v (%T)", rq.UserInfo[0], rq.UserInfo[0])
	}
}

func TestReadAsynchronousOperationsWindowValidatesLengthAndIgnoresReserved(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{name: "reserved non-zero", data: []byte{SubItemAsynchronousOperations, 0x7f, 0, 4, 0, 2, 0, 3}},
		{name: "short", data: []byte{SubItemAsynchronousOperations, 0, 0, 3, 0, 2, 0}, wantErr: true},
		{name: "long", data: []byte{SubItemAsynchronousOperations, 0, 0, 5, 0, 2, 0, 3, 0}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items, err := readUserInformation(test.data)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidUserItem) {
					t.Fatalf("error = %v, want ErrInvalidUserItem", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0] != (AsynchronousOperationsWindow{MaximumInvoked: 2, MaximumPerformed: 3}) {
				t.Fatalf("items = %#v", items)
			}
		})
	}
}

func TestPDURoundTripTable(t *testing.T) {
	tests := []struct {
		name string
		pdu  PDU
	}{
		{
			name: "associate request",
			pdu: AssociationRQ{
				ProtocolVersion:        1,
				CalledAETitle:          "SCP_AE",
				CallingAETitle:         "SCU_AE",
				ApplicationContextName: "1.2.840.10008.3.1.1.1",
				PresentationContexts: []PresentationContextProposed{
					{
						ID:                 1,
						AbstractSyntaxUID:  "1.2.840.10008.1.1",
						TransferSyntaxUIDs: []string{"1.2.840.10008.1.2", "1.2.840.10008.1.2.1"},
					},
					{
						ID:                 3,
						AbstractSyntaxUID:  "1.2.840.10008.5.1.4.1.1.2",
						TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"},
					},
				},
				UserInfo: []UserVariableItem{
					MaxLengthItem{Value: 16_384},
					ImplementationClassUIDItem{UID: "1.2.826.0.1.3680043.10.100"},
					ImplementationVersionNameItem{Name: "DICOM_GO"},
					SopClassExtendedNegotiationItem{SopClassUID: "1.2.840.10008.1.1", Data: []byte{1, 0, 1, 1}},
					RoleSelectionItem{SopClassUID: "1.2.840.10008.1.1", SCURole: true, SCPRole: false},
					UserIdentityItem{Type: UserIdentityUsernamePassword, PrimaryField: []byte("user"), SecondaryField: []byte("pass")},
				},
			},
		},
		{
			name: "associate accept",
			pdu: AssociationAC{
				ProtocolVersion:        1,
				CalledAETitle:          "SCP_AE",
				CallingAETitle:         "SCU_AE",
				ApplicationContextName: "1.2.840.10008.3.1.1.1",
				PresentationContexts: []PresentationContextResult{
					{ID: 1, Result: PresentationContextAcceptance, TransferSyntaxUID: "1.2.840.10008.1.2"},
					{ID: 3, Result: PresentationContextNoReason, TransferSyntaxUID: "1.2.840.10008.1.2.1"},
				},
				UserInfo: []UserVariableItem{MaxLengthItem{Value: 4096}},
			},
		},
		{
			name: "associate accept with user identity response",
			pdu: AssociationAC{
				ProtocolVersion:        1,
				CalledAETitle:          "SCP_AE",
				CallingAETitle:         "SCU_AE",
				ApplicationContextName: "1.2.840.10008.3.1.1.1",
				PresentationContexts: []PresentationContextResult{
					{ID: 1, Result: PresentationContextAcceptance, TransferSyntaxUID: "1.2.840.10008.1.2"},
				},
				UserInfo: []UserVariableItem{
					MaxLengthItem{Value: 4096},
					UserIdentityResponseItem{ServerResponse: []byte("accepted")},
				},
			},
		},
		{
			name: "associate reject service user",
			pdu:  AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceUser, Reason: 1},
		},
		{
			name: "associate reject acse provider",
			pdu:  AssociationRJ{Result: AssociateRJResultTransient, Source: AssociateRJSourceServiceProviderACSE, Reason: 2},
		},
		{
			name: "associate reject presentation provider",
			pdu:  AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceProviderPresentation, Reason: 2},
		},
		{
			name: "pdata single command",
			pdu:  PDataTF{Values: []PDataValue{{PresentationContextID: 1, IsCommand: true, IsLast: true, Data: []byte{0x01, 0x02}}}},
		},
		{
			name: "pdata command and data",
			pdu: PDataTF{Values: []PDataValue{
				{PresentationContextID: 1, IsCommand: true, IsLast: false, Data: []byte{0x10}},
				{PresentationContextID: 3, IsCommand: false, IsLast: true, Data: []byte{0x20, 0x21}},
			}},
		},
		{name: "release request", pdu: ReleaseRQ{}},
		{name: "release response", pdu: ReleaseRP{}},
		{name: "abort service user", pdu: AbortRQ{Source: AbortSourceServiceUser, Reason: 0}},
		{name: "abort service provider", pdu: AbortRQ{Source: AbortSourceServiceProvider, Reason: AbortReasonUnexpectedPDU}},
		{name: "unknown pdu", pdu: UnknownPDU{Type: 0x99, Data: []byte{0xde, 0xad, 0xbe, 0xef}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WritePDU(&buf, tt.pdu); err != nil {
				t.Fatalf("WritePDU() error = %v", err)
			}
			got, err := ReadPDU(bytes.NewReader(buf.Bytes()), DefaultMaxPDU)
			if err != nil {
				t.Fatalf("ReadPDU() error = %v", err)
			}
			if !reflect.DeepEqual(got, ptrPDU(tt.pdu)) {
				t.Fatalf("round-trip mismatch:\ngot  %#v\nwant %#v", got, ptrPDU(tt.pdu))
			}
		})
	}
}

func TestAssociationACRoundTrip(t *testing.T) {
	pdu := AssociationAC{
		ProtocolVersion:        1,
		CalledAETitle:          "SCP",
		CallingAETitle:         "SCU",
		ApplicationContextName: "1.2.840.10008.3.1.1.1",
		PresentationContexts: []PresentationContextResult{
			{ID: 1, Result: PresentationContextAcceptance, TransferSyntaxUID: "1.2.840.10008.1.2"},
			{ID: 3, Result: PresentationContextTransferSyntaxesNotSupported, TransferSyntaxUID: "1.2.840.10008.1.2.1"},
		},
		UserInfo: []UserVariableItem{MaxLengthItem{Value: 4096}},
	}

	var buf bytes.Buffer
	if err := WritePDU(&buf, pdu); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	got, err := ReadPDU(bytes.NewReader(buf.Bytes()), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	if !reflect.DeepEqual(got, &pdu) {
		t.Fatalf("round-trip mismatch:\ngot  %#v\nwant %#v", got, pdu)
	}
}

func TestPDataMessageControlHeaderEncoding(t *testing.T) {
	pdu := PDataTF{Values: []PDataValue{
		{PresentationContextID: 1, IsCommand: true, IsLast: false, Data: []byte{0xaa}},
		{PresentationContextID: 3, IsCommand: false, IsLast: true, Data: []byte{0xbb, 0xcc}},
	}}

	var buf bytes.Buffer
	if err := WritePDU(&buf, pdu); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	data := buf.Bytes()
	if got, want := data[0], byte(PDUDataTF); got != want {
		t.Fatalf("PDU type = 0x%02x, want 0x%02x", got, want)
	}
	if got, want := binary.BigEndian.Uint32(data[6:10]), uint32(3); got != want {
		t.Fatalf("first PDV length = %d, want %d", got, want)
	}
	if got, want := data[10], byte(1); got != want {
		t.Fatalf("first PDV presentation context ID = %d, want %d", got, want)
	}
	if got, want := data[11], byte(0x01); got != want {
		t.Fatalf("first PDV flags = 0x%02x, want 0x%02x", got, want)
	}
	if got, want := binary.BigEndian.Uint32(data[13:17]), uint32(4); got != want {
		t.Fatalf("second PDV length = %d, want %d", got, want)
	}
	if got, want := data[17], byte(3); got != want {
		t.Fatalf("second PDV presentation context ID = %d, want %d", got, want)
	}
	if got, want := data[18], byte(0x02); got != want {
		t.Fatalf("second PDV flags = 0x%02x, want 0x%02x", got, want)
	}

	got, err := ReadPDU(bytes.NewReader(data), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	if !reflect.DeepEqual(got, &pdu) {
		t.Fatalf("decoded = %#v, want %#v", got, &pdu)
	}
}

func TestPDataRoundTrip(t *testing.T) {
	pdu := PDataTF{Values: []PDataValue{
		{PresentationContextID: 1, IsCommand: true, IsLast: true, Data: []byte{0, 1, 2, 3}},
		{PresentationContextID: 3, IsCommand: false, IsLast: true, Data: []byte{4, 5}},
	}}

	var buf bytes.Buffer
	if err := WritePDU(&buf, pdu); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	got, err := ReadPDU(bytes.NewReader(buf.Bytes()), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	if !reflect.DeepEqual(got, &pdu) {
		t.Fatalf("round-trip mismatch:\ngot  %#v\nwant %#v", got, pdu)
	}
}

func TestUnknownUserItemRoundTrip(t *testing.T) {
	pdu := AssociationRQ{
		ProtocolVersion:        1,
		CalledAETitle:          "SCP",
		CallingAETitle:         "SCU",
		ApplicationContextName: "1.2.840.10008.3.1.1.1",
		PresentationContexts: []PresentationContextProposed{
			{ID: 1, AbstractSyntaxUID: "1.2.840.10008.1.1", TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"}},
		},
		UserInfo: []UserVariableItem{UnknownUserItem{Type: 0x99, Data: []byte{1, 2, 3, 4}}},
	}

	var buf bytes.Buffer
	if err := WritePDU(&buf, pdu); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	got, err := ReadPDU(bytes.NewReader(buf.Bytes()), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	rq, ok := got.(*AssociationRQ)
	if !ok {
		t.Fatalf("decoded type = %T, want *AssociationRQ", got)
	}
	if len(rq.UserInfo) != 1 {
		t.Fatalf("len(UserInfo) = %d, want 1", len(rq.UserInfo))
	}
	unknown, ok := rq.UserInfo[0].(UnknownUserItem)
	if !ok {
		t.Fatalf("UserInfo[0] = %T, want UnknownUserItem", rq.UserInfo[0])
	}
	if unknown.Type != 0x99 || !bytes.Equal(unknown.Data, []byte{1, 2, 3, 4}) {
		t.Fatalf("unknown user item = %#v, want type 0x99 data [1 2 3 4]", unknown)
	}
}

func TestFixedPDUBytes(t *testing.T) {
	tests := []struct {
		name string
		pdu  PDU
		want []byte
	}{
		{
			name: "associate reject",
			pdu:  AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceUser, Reason: 7},
			want: []byte{0x03, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x01, 0x01, 0x07},
		},
		{
			name: "release request",
			pdu:  ReleaseRQ{},
			want: []byte{0x05, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00},
		},
		{
			name: "release response",
			pdu:  ReleaseRP{},
			want: []byte{0x06, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00},
		},
		{
			name: "abort",
			pdu:  AbortRQ{Source: AbortSourceServiceProvider, Reason: AbortReasonInvalidPDUParameter},
			want: []byte{0x07, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x02, 0x06},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WritePDU(&buf, tt.pdu); err != nil {
				t.Fatalf("WritePDU() error = %v", err)
			}
			if got := buf.Bytes(); !bytes.Equal(got, tt.want) {
				t.Fatalf("bytes = %v, want %v", got, tt.want)
			}
			decoded, err := ReadPDU(bytes.NewReader(tt.want), DefaultMaxPDU)
			if err != nil {
				t.Fatalf("ReadPDU() error = %v", err)
			}
			if !reflect.DeepEqual(decoded, ptrPDU(tt.pdu)) {
				t.Fatalf("decoded = %#v, want %#v", decoded, tt.pdu)
			}
		})
	}
}

func TestAETitlePaddingAndTrimming(t *testing.T) {
	pdu := AssociationRQ{
		ProtocolVersion:        1,
		CalledAETitle:          "SCP",
		CallingAETitle:         "SCU",
		ApplicationContextName: "1.2.840.10008.3.1.1.1",
		PresentationContexts: []PresentationContextProposed{
			{ID: 1, AbstractSyntaxUID: "1.2.840.10008.1.1", TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"}},
		},
		UserInfo: []UserVariableItem{MaxLengthItem{Value: 2048}},
	}

	var buf bytes.Buffer
	if err := WritePDU(&buf, pdu); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	data := buf.Bytes()
	if got, want := string(data[10:26]), "SCP             "; got != want {
		t.Fatalf("called AE bytes = %q, want %q", got, want)
	}
	if got, want := string(data[26:42]), "SCU             "; got != want {
		t.Fatalf("calling AE bytes = %q, want %q", got, want)
	}

	got, err := ReadPDU(bytes.NewReader(data), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	rq, ok := got.(*AssociationRQ)
	if !ok {
		t.Fatalf("decoded type = %T, want *AssociationRQ", got)
	}
	if rq.CalledAETitle != "SCP" || rq.CallingAETitle != "SCU" {
		t.Fatalf("AE titles = %q/%q, want SCP/SCU", rq.CalledAETitle, rq.CallingAETitle)
	}
}

func TestReadPDUIgnoresReservedFields(t *testing.T) {
	pdu := AssociationRQ{
		ProtocolVersion:        1,
		CalledAETitle:          "SCP",
		CallingAETitle:         "SCU",
		ApplicationContextName: "1.2.840.10008.3.1.1.1",
		PresentationContexts: []PresentationContextProposed{
			{ID: 1, AbstractSyntaxUID: "1.2.840.10008.1.1", TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"}},
		},
		UserInfo: []UserVariableItem{MaxLengthItem{Value: 2048}},
	}

	var buf bytes.Buffer
	if err := WritePDU(&buf, pdu); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	data := append([]byte(nil), buf.Bytes()...)
	data[1] = 0xff  // PDU header reserved byte.
	data[8] = 0xaa  // Association fixed reserved field.
	data[9] = 0xbb  // Association fixed reserved field.
	data[42] = 0xcc // First byte of 32 reserved bytes.
	data[75] = 0xdd // Application context item reserved byte.

	got, err := ReadPDU(bytes.NewReader(data), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	if !reflect.DeepEqual(got, &pdu) {
		t.Fatalf("decoded = %#v, want %#v", got, pdu)
	}
}

func TestReadPDUIgnoresUnknownAssociationItems(t *testing.T) {
	var body bytes.Buffer
	if err := writeAssociationFixed(&body, 1, "SCP", "SCU"); err != nil {
		t.Fatalf("writeAssociationFixed() error = %v", err)
	}
	if err := writeItem(&body, 0x99, []byte{1, 2, 3}); err != nil {
		t.Fatalf("writeItem(unknown) error = %v", err)
	}
	if err := writeApplicationContext(&body, "1.2.840.10008.3.1.1.1"); err != nil {
		t.Fatalf("writeApplicationContext() error = %v", err)
	}
	pc := PresentationContextProposed{ID: 1, AbstractSyntaxUID: "1.2.840.10008.1.1", TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"}}
	if err := writePresentationContextProposed(&body, &pc); err != nil {
		t.Fatalf("writePresentationContextProposed() error = %v", err)
	}
	if err := writeUserInformation(&body, []UserVariableItem{MaxLengthItem{Value: 2048}}); err != nil {
		t.Fatalf("writeUserInformation() error = %v", err)
	}

	got, err := ReadPDU(bytes.NewReader(rawPDU(PDUAssociateRQ, body.Bytes())), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	rq, ok := got.(*AssociationRQ)
	if !ok {
		t.Fatalf("decoded type = %T, want *AssociationRQ", got)
	}
	if rq.ApplicationContextName != "1.2.840.10008.3.1.1.1" || len(rq.PresentationContexts) != 1 {
		t.Fatalf("decoded association = %#v", rq)
	}
}

func TestReadVariableItemsRejectsDuplicateOrEmptySingletons(t *testing.T) {
	tests := []struct {
		name  string
		build func(*bytes.Buffer)
	}{
		{
			name: "duplicate application context",
			build: func(body *bytes.Buffer) {
				if err := writeApplicationContext(body, "1.2.840.10008.3.1.1.1"); err != nil {
					t.Fatalf("writeApplicationContext() error = %v", err)
				}
				if err := writeApplicationContext(body, "1.2.840.10008.3.1.1.1"); err != nil {
					t.Fatalf("writeApplicationContext() error = %v", err)
				}
			},
		},
		{
			name: "empty application context",
			build: func(body *bytes.Buffer) {
				if err := writeItem(body, ItemApplicationContext, []byte("   ")); err != nil {
					t.Fatalf("writeItem(application context) error = %v", err)
				}
			},
		},
		{
			name: "duplicate user information",
			build: func(body *bytes.Buffer) {
				if err := writeApplicationContext(body, "1.2.840.10008.3.1.1.1"); err != nil {
					t.Fatalf("writeApplicationContext() error = %v", err)
				}
				if err := writeUserInformation(body, []UserVariableItem{MaxLengthItem{Value: 2048}}); err != nil {
					t.Fatalf("writeUserInformation() error = %v", err)
				}
				if err := writeUserInformation(body, []UserVariableItem{MaxLengthItem{Value: 4096}}); err != nil {
					t.Fatalf("writeUserInformation() error = %v", err)
				}
			},
		},
		{
			name: "empty user information",
			build: func(body *bytes.Buffer) {
				if err := writeApplicationContext(body, "1.2.840.10008.3.1.1.1"); err != nil {
					t.Fatalf("writeApplicationContext() error = %v", err)
				}
				if err := writeUserInformation(body, []UserVariableItem{}); err != nil {
					t.Fatalf("writeUserInformation() error = %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body bytes.Buffer
			tt.build(&body)
			_, _, _, err := readVariableItems(body.Bytes(), false)
			if !errors.Is(err, ErrInvalidPDUItem) {
				t.Fatalf("readVariableItems() error = %v, want ErrInvalidPDUItem", err)
			}
		})
	}
}

func TestReadPDUTrimsApplicationContext(t *testing.T) {
	var body bytes.Buffer
	if err := writeAssociationFixed(&body, 1, "SCP", "SCU"); err != nil {
		t.Fatalf("writeAssociationFixed() error = %v", err)
	}
	if err := writeItem(&body, ItemApplicationContext, []byte("  1.2.840.10008.3.1.1.1  ")); err != nil {
		t.Fatalf("writeItem(application context) error = %v", err)
	}
	pc := PresentationContextProposed{ID: 1, AbstractSyntaxUID: "1.2.840.10008.1.1", TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"}}
	if err := writePresentationContextProposed(&body, &pc); err != nil {
		t.Fatalf("writePresentationContextProposed() error = %v", err)
	}
	if err := writeUserInformation(&body, []UserVariableItem{MaxLengthItem{Value: 2048}}); err != nil {
		t.Fatalf("writeUserInformation() error = %v", err)
	}

	got, err := ReadPDU(bytes.NewReader(rawPDU(PDUAssociateRQ, body.Bytes())), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	rq, ok := got.(*AssociationRQ)
	if !ok {
		t.Fatalf("decoded type = %T, want *AssociationRQ", got)
	}
	if rq.ApplicationContextName != "1.2.840.10008.3.1.1.1" {
		t.Fatalf("ApplicationContextName = %q, want trimmed UID", rq.ApplicationContextName)
	}
}

func TestReadPDUIgnoresUnknownPresentationContextSubItems(t *testing.T) {
	var pcItem bytes.Buffer
	pcItem.Write([]byte{1, 0, 0, 0})
	if err := writeItem(&pcItem, 0x99, []byte{9, 8, 7}); err != nil {
		t.Fatalf("writeItem(unknown) error = %v", err)
	}
	if err := writeItem(&pcItem, ItemAbstractSyntax, []byte("1.2.840.10008.1.1")); err != nil {
		t.Fatalf("writeItem(abstract) error = %v", err)
	}
	if err := writeItem(&pcItem, ItemTransferSyntax, []byte("1.2.840.10008.1.2")); err != nil {
		t.Fatalf("writeItem(transfer) error = %v", err)
	}

	var body bytes.Buffer
	if err := writeAssociationFixed(&body, 1, "SCP", "SCU"); err != nil {
		t.Fatalf("writeAssociationFixed() error = %v", err)
	}
	if err := writeApplicationContext(&body, "1.2.840.10008.3.1.1.1"); err != nil {
		t.Fatalf("writeApplicationContext() error = %v", err)
	}
	if err := writeItem(&body, ItemPresentationContextRQ, pcItem.Bytes()); err != nil {
		t.Fatalf("writeItem(pc) error = %v", err)
	}
	if err := writeUserInformation(&body, []UserVariableItem{MaxLengthItem{Value: 2048}}); err != nil {
		t.Fatalf("writeUserInformation() error = %v", err)
	}

	got, err := ReadPDU(bytes.NewReader(rawPDU(PDUAssociateRQ, body.Bytes())), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	rq, ok := got.(*AssociationRQ)
	if !ok {
		t.Fatalf("decoded type = %T, want *AssociationRQ", got)
	}
	if len(rq.PresentationContexts) != 1 || rq.PresentationContexts[0].AbstractSyntaxUID != "1.2.840.10008.1.1" {
		t.Fatalf("decoded presentation contexts = %#v", rq.PresentationContexts)
	}
}

func TestReadPresentationContextResultTransferSyntaxRequiredOnlyWhenAccepted(t *testing.T) {
	rejected, err := readPresentationContextResult([]byte{1, 0, PresentationContextNoReason, 0})
	if err != nil {
		t.Fatalf("readPresentationContextResult(rejected) error = %v", err)
	}
	if rejected.TransferSyntaxUID != "" {
		t.Fatalf("rejected TransferSyntaxUID = %q, want empty", rejected.TransferSyntaxUID)
	}

	_, err = readPresentationContextResult([]byte{1, 0, PresentationContextAcceptance, 0})
	if !errors.Is(err, ErrMissingPDUField) {
		t.Fatalf("readPresentationContextResult(accepted) error = %v, want ErrMissingPDUField", err)
	}
}

func TestReadPDUAllowsAssociationProtocolVersionForNegotiation(t *testing.T) {
	var body bytes.Buffer
	if err := writeAssociationFixed(&body, 2, "SCP", "SCU"); err != nil {
		t.Fatalf("writeAssociationFixed() error = %v", err)
	}
	if err := writeApplicationContext(&body, "1.2.840.10008.3.1.1.1"); err != nil {
		t.Fatalf("writeApplicationContext() error = %v", err)
	}
	pc := PresentationContextProposed{ID: 1, AbstractSyntaxUID: "1.2.840.10008.1.1", TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"}}
	if err := writePresentationContextProposed(&body, &pc); err != nil {
		t.Fatalf("writePresentationContextProposed() error = %v", err)
	}
	if err := writeUserInformation(&body, []UserVariableItem{MaxLengthItem{Value: 2048}}); err != nil {
		t.Fatalf("writeUserInformation() error = %v", err)
	}

	got, err := ReadPDU(bytes.NewReader(rawPDU(PDUAssociateRQ, body.Bytes())), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	rq, ok := got.(*AssociationRQ)
	if !ok {
		t.Fatalf("decoded type = %T, want *AssociationRQ", got)
	}
	if rq.ProtocolVersion != 2 {
		t.Fatalf("ProtocolVersion = %d, want 2", rq.ProtocolVersion)
	}
}

func TestUnknownPDURoundTrip(t *testing.T) {
	pdu := &UnknownPDU{Type: 0x88, Data: []byte{0x01, 0x02, 0x03}}
	var buf bytes.Buffer
	if err := WritePDU(&buf, pdu); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	got, err := ReadPDU(bytes.NewReader(buf.Bytes()), DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	if !reflect.DeepEqual(got, pdu) {
		t.Fatalf("decoded = %#v, want %#v", got, pdu)
	}
}

func TestAbortReservedSourceRejected(t *testing.T) {
	_, err := ReadPDU(bytes.NewReader(rawPDU(PDUAbort, []byte{0, 0, AbortSourceReserved, 0})), DefaultMaxPDU)
	if !errors.Is(err, ErrInvalidPDUField) {
		t.Fatalf("ReadPDU(reserved abort source) error = %v, want ErrInvalidPDUField", err)
	}

	var buf bytes.Buffer
	err = WritePDU(&buf, AbortRQ{Source: AbortSourceReserved, Reason: 0})
	if !errors.Is(err, ErrInvalidPDUField) {
		t.Fatalf("WritePDU(reserved abort source) error = %v, want ErrInvalidPDUField", err)
	}
}

func TestReadAbortRejectsTrailingBytes(t *testing.T) {
	_, err := readAbortRQ([]byte{0, 0, AbortSourceServiceUser, 0, 0})
	if !errors.Is(err, ErrInvalidPDUField) {
		t.Fatalf("readAbortRQ() error = %v, want ErrInvalidPDUField", err)
	}
}

func TestPDUTypeOfTypedNil(t *testing.T) {
	var rq *AssociationRQ
	if got := PDUTypeOf(rq); got != 0 {
		t.Fatalf("PDUTypeOf((*AssociationRQ)(nil)) = %d, want 0", got)
	}
}

func TestReadPDUSizeLimit(t *testing.T) {
	var header [PDUHeaderSize]byte
	header[0] = byte(PDUDataTF)
	binary.BigEndian.PutUint32(header[2:], DefaultMaxPDU+1)

	_, err := ReadPDU(bytes.NewReader(header[:]), DefaultMaxPDU)
	if !errors.Is(err, ErrPDUTooLarge) {
		t.Fatalf("ReadPDU() error = %v, want ErrPDUTooLarge", err)
	}
}

func TestReadPDUAllowsAssociationRequestAbovePDataMax(t *testing.T) {
	contexts := make([]PresentationContextProposed, 40)
	for i := range contexts {
		contexts[i] = PresentationContextProposed{
			ID:                 byte(2*i + 1),
			AbstractSyntaxUID:  "1.2.840.10008.5.1.4.1.1.2",
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
		}
	}
	rq := AssociationRQ{
		ProtocolVersion:        1,
		CalledAETitle:          "SCP",
		CallingAETitle:         "SCU",
		ApplicationContextName: ApplicationContextName,
		PresentationContexts:   contexts,
		UserInfo: []UserVariableItem{
			MaxLengthItem{Value: DefaultMaxPDU},
			ImplementationClassUIDItem{UID: ImplementationClassUID},
		},
	}

	var buf bytes.Buffer
	if err := WritePDU(&buf, rq); err != nil {
		t.Fatalf("WritePDU() error = %v", err)
	}
	pduLength := uint32(buf.Len()) - PDUHeaderSize
	if pduLength <= MinimumPDUSize {
		t.Fatalf("test request PDU length = %d, want above small P-DATA max %d", pduLength, MinimumPDUSize)
	}

	got, err := ReadPDU(bytes.NewReader(buf.Bytes()), MinimumPDUSize)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	if _, ok := got.(*AssociationRQ); !ok {
		t.Fatalf("ReadPDU() = %T, want *AssociationRQ", got)
	}
}

func TestReadPDURejectsAssociationRequestAboveAssociationReadLimit(t *testing.T) {
	var header [PDUHeaderSize]byte
	header[0] = byte(PDUAssociateRQ)
	binary.BigEndian.PutUint32(header[2:], maxAssociationPDU+1)

	_, err := ReadPDU(bytes.NewReader(header[:]), MinimumPDUSize)
	if !errors.Is(err, ErrPDUTooLarge) {
		t.Fatalf("ReadPDU() error = %v, want ErrPDUTooLarge", err)
	}
}

func TestAssociationWriterRequiresMandatoryItems(t *testing.T) {
	tests := []struct {
		name string
		pdu  PDU
	}{
		{
			name: "request missing presentation contexts",
			pdu: AssociationRQ{
				ProtocolVersion:        1,
				CalledAETitle:          "SCP",
				CallingAETitle:         "SCU",
				ApplicationContextName: "1.2.840.10008.3.1.1.1",
				UserInfo:               []UserVariableItem{},
			},
		},
		{
			name: "request missing user information",
			pdu: AssociationRQ{
				ProtocolVersion:        1,
				CalledAETitle:          "SCP",
				CallingAETitle:         "SCU",
				ApplicationContextName: "1.2.840.10008.3.1.1.1",
				PresentationContexts: []PresentationContextProposed{
					{ID: 1, AbstractSyntaxUID: "1.2.840.10008.1.1", TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"}},
				},
			},
		},
		{
			name: "accept missing presentation contexts",
			pdu: AssociationAC{
				ProtocolVersion:        1,
				CalledAETitle:          "SCP",
				CallingAETitle:         "SCU",
				ApplicationContextName: "1.2.840.10008.3.1.1.1",
				UserInfo:               []UserVariableItem{},
			},
		},
		{
			name: "accept missing user information",
			pdu: AssociationAC{
				ProtocolVersion:        1,
				CalledAETitle:          "SCP",
				CallingAETitle:         "SCU",
				ApplicationContextName: "1.2.840.10008.3.1.1.1",
				PresentationContexts: []PresentationContextResult{
					{ID: 1, Result: PresentationContextAcceptance, TransferSyntaxUID: "1.2.840.10008.1.2"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := WritePDU(&buf, tt.pdu)
			if !errors.Is(err, ErrInvalidPDU) {
				t.Fatalf("WritePDU() error = %v, want ErrInvalidPDU", err)
			}
		})
	}
}

func TestPDUReaderRejectsTruncatedBodies(t *testing.T) {
	tests := []struct {
		name string
		read func([]byte) error
		data []byte
	}{
		{
			name: "associate request",
			read: func(data []byte) error {
				_, err := readAssociationRQ(data)
				return err
			},
			data: make([]byte, 67),
		},
		{
			name: "associate accept",
			read: func(data []byte) error {
				_, err := readAssociationAC(data)
				return err
			},
			data: make([]byte, 67),
		},
		{
			name: "associate reject",
			read: func(data []byte) error {
				_, err := readAssociationRJ(data)
				return err
			},
			data: make([]byte, 3),
		},
		{
			name: "pdata",
			read: func(data []byte) error {
				_, err := readPDataTF(data)
				return err
			},
			data: make([]byte, 5),
		},
		{
			name: "release request",
			read: func(data []byte) error {
				_, err := readReleaseRQ(data)
				return err
			},
			data: make([]byte, 3),
		},
		{
			name: "release response",
			read: func(data []byte) error {
				_, err := readReleaseRP(data)
				return err
			},
			data: make([]byte, 3),
		},
		{
			name: "abort",
			read: func(data []byte) error {
				_, err := readAbortRQ(data)
				return err
			},
			data: make([]byte, 3),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.read(tt.data); err == nil {
				t.Fatal("reader returned nil error for truncated body")
			}
		})
	}
}

func TestReadAssociationRJRejectsTrailingBytes(t *testing.T) {
	_, err := readAssociationRJ([]byte{0, AssociateRJResultPermanent, AssociateRJSourceServiceUser, 1, 0})
	if !errors.Is(err, ErrInvalidPDUField) {
		t.Fatalf("readAssociationRJ() error = %v, want ErrInvalidPDUField", err)
	}
}

func TestPDataRejectsEmptyBody(t *testing.T) {
	_, err := readPDataTF(nil)
	if !errors.Is(err, ErrInvalidPDUField) {
		t.Fatalf("readPDataTF(nil) error = %v, want ErrInvalidPDUField", err)
	}
}

func TestPDataRejectsInvalidPDVLength(t *testing.T) {
	data := []byte{0, 0, 0, 1, 1}
	_, err := readPDataTF(data)
	if !errors.Is(err, ErrInvalidPDUField) {
		t.Fatalf("readPDataTF() error = %v, want ErrInvalidPDUField", err)
	}
}

func TestRejectsMalformedFields(t *testing.T) {
	tests := []struct {
		name string
		pdu  PDU
		want error
	}{
		{
			name: "too long AE title",
			pdu: AssociationRQ{
				ProtocolVersion:        1,
				CalledAETitle:          "THIS_AE_TITLE_IS_TOO_LONG",
				CallingAETitle:         "SCU",
				ApplicationContextName: "1.2.840.10008.3.1.1.1",
				PresentationContexts: []PresentationContextProposed{
					{ID: 1, AbstractSyntaxUID: "1.2.840.10008.1.1", TransferSyntaxUIDs: []string{"1.2.840.10008.1.2"}},
				},
				UserInfo: []UserVariableItem{},
			},
			want: ErrInvalidAEtitle,
		},
		{
			name: "even presentation context id",
			pdu: PDataTF{Values: []PDataValue{
				{PresentationContextID: 2, IsCommand: true, IsLast: true, Data: []byte{1}},
			}},
			want: ErrInvalidPCID,
		},
		{
			name: "duplicate presentation context id",
			pdu: AssociationRQ{
				ProtocolVersion: DefaultProtocolVersion, CalledAETitle: "SCP", CallingAETitle: "SCU",
				ApplicationContextName: ApplicationContextName,
				PresentationContexts: []PresentationContextProposed{
					{ID: 1, AbstractSyntaxUID: "1.2.3", TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
					{ID: 1, AbstractSyntaxUID: "1.2.4", TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
				},
				UserInfo: []UserVariableItem{MaxLengthItem{Value: 4096}},
			},
			want: ErrInvalidPCID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := WritePDU(&buf, tt.pdu)
			if !errors.Is(err, tt.want) {
				t.Fatalf("WritePDU() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func ptrPDU(pdu PDU) PDU {
	switch p := pdu.(type) {
	case AssociationRQ:
		return &p
	case AssociationAC:
		return &p
	case AssociationRJ:
		return &p
	case PDataTF:
		return &p
	case ReleaseRQ:
		return &p
	case ReleaseRP:
		return &p
	case AbortRQ:
		return &p
	case UnknownPDU:
		return &p
	default:
		return pdu
	}
}

func rawPDU(pduType PDUType, body []byte) []byte {
	var out bytes.Buffer
	out.WriteByte(byte(pduType))
	out.WriteByte(0)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(body)))
	out.Write(length[:])
	out.Write(body)
	return out.Bytes()
}
