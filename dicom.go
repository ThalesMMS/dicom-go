package dicom

import (
	"io"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type File = object.File
type Object = object.Object
type ReadFileOptions = object.ReadFileOptions

func OpenFile(path string) (*File, error) {
	return object.OpenFile(path)
}

func OpenFileWithOptions(path string, opts ReadFileOptions) (*File, error) {
	return object.OpenFileWithOptions(path, opts)
}

func ReadFileWithOptions(r io.Reader, opts ReadFileOptions) (*File, error) {
	return object.ReadFileWithOptions(r, opts)
}

func ReadDataSet(r io.Reader, syntax transfer.Syntax) (*Object, error) {
	return object.ReadDataSet(r, syntax)
}

func ReadDataSetWithOptions(r io.Reader, syntax transfer.Syntax, opts ReadFileOptions) (*Object, error) {
	return object.ReadDataSetWithOptions(r, syntax, opts)
}

func OpenDataSet(path string, syntax transfer.Syntax) (*Object, error) {
	return object.OpenDataSet(path, syntax)
}

func OpenDataSetWithOptions(path string, syntax transfer.Syntax, opts ReadFileOptions) (*Object, error) {
	return object.OpenDataSetWithOptions(path, syntax, opts)
}
