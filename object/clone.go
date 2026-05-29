package object

import "bytes"

// CloneFile returns an independent copy of src by serializing and reparsing it.
func CloneFile(src *File) (*File, error) {
	if src == nil {
		return nil, ErrNilFile
	}
	var buf bytes.Buffer
	if err := WriteFile(&buf, src); err != nil {
		return nil, err
	}
	return ReadFile(bytes.NewReader(buf.Bytes()))
}
