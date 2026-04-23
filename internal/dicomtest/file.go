package dicomtest

import (
	"bytes"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type FileBuilder struct {
	meta     *FileMetaBuilder
	elements []core.Element
}

func NewFileBuilder() *FileBuilder {
	return &FileBuilder{meta: NewFileMetaBuilder()}
}

func (b *FileBuilder) WithMeta(meta *FileMetaBuilder) *FileBuilder {
	if meta == nil {
		b.meta = NewFileMetaBuilder()
		return b
	}
	b.meta = meta
	return b
}

func (b *FileBuilder) AddElement(elem core.Element) *FileBuilder {
	b.elements = append(b.elements, elem)
	return b
}

func (b *FileBuilder) AddElements(elems ...core.Element) *FileBuilder {
	b.elements = append(b.elements, elems...)
	return b
}

func (b *FileBuilder) Build() ([]byte, error) {
	meta := b.meta
	if meta == nil {
		meta = NewFileMetaBuilder()
	}

	syntax, ok := transfer.DefaultRegistry.Get(meta.TransferSyntaxUID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, meta.TransferSyntaxUID)
	}

	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(meta.Encode())
	buf.Write(EncodeElements(syntax, b.elements...))
	return buf.Bytes(), nil
}
