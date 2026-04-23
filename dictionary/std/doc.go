// Package std exposes the generated standard DICOM dictionary.
//
// Regenerate with:
//
//	go generate ./dictionary/std
package std

//go:generate go run ../../cmd/dicomdictgen -input ../../internal/standard/dicom.dic -output std_gen.go
