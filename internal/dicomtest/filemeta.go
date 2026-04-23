package dicomtest

import (
	"encoding/binary"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type FileMetaBuilder struct {
	MediaStorageSOPClassUID    string
	MediaStorageSOPInstanceUID string
	TransferSyntaxUID          string
	ImplementationClassUID     string
	ImplementationVersionName  string
}

func NewFileMetaBuilder() *FileMetaBuilder {
	return &FileMetaBuilder{
		MediaStorageSOPClassUID:    TestSOPClassUID,
		MediaStorageSOPInstanceUID: TestSOPInstanceUID,
		TransferSyntaxUID:          transfer.ExplicitVRLittleEndian.UID,
		ImplementationClassUID:     ImplementationClassUID,
		ImplementationVersionName:  ImplementationVersionName,
	}
}

func (b *FileMetaBuilder) WithTransferSyntax(uid string) *FileMetaBuilder {
	builder := b.normalized()
	builder.TransferSyntaxUID = uid
	return builder
}

func (b *FileMetaBuilder) WithSOPClass(uid string) *FileMetaBuilder {
	builder := b.normalized()
	builder.MediaStorageSOPClassUID = uid
	return builder
}

func (b *FileMetaBuilder) WithSOPInstance(uid string) *FileMetaBuilder {
	builder := b.normalized()
	builder.MediaStorageSOPInstanceUID = uid
	return builder
}

func (b *FileMetaBuilder) Build() []core.Element {
	builder := b.normalized()

	metaWithoutGroupLength := []core.Element{
		NewOBElement(tagFileMetaInformationVersion, []byte{0x00, 0x01}),
		NewUIElement(tagMediaStorageSOPClassUID, builder.MediaStorageSOPClassUID),
		NewUIElement(tagMediaStorageSOPInstanceUID, builder.MediaStorageSOPInstanceUID),
		NewUIElement(tagTransferSyntaxUID, builder.TransferSyntaxUID),
		NewUIElement(tagImplementationClassUID, builder.ImplementationClassUID),
		NewStringElement(tagImplementationVersionName, core.VRSH, builder.ImplementationVersionName),
	}

	groupLength := Uint32Element(
		tagFileMetaInformationGroupLength,
		core.VRUL,
		binary.LittleEndian,
		uint32(len(EncodeElements(transfer.ExplicitVRLittleEndian, metaWithoutGroupLength...))),
	)

	return append([]core.Element{groupLength}, metaWithoutGroupLength...)
}

func (b *FileMetaBuilder) Encode() []byte {
	return EncodeElements(transfer.ExplicitVRLittleEndian, b.Build()...)
}

func (b *FileMetaBuilder) normalized() *FileMetaBuilder {
	if b == nil {
		return NewFileMetaBuilder()
	}
	return b
}
