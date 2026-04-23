// Package uid exposes the generated standard DICOM UID registry.
//
// Regenerate with:
//
//	go generate ./dictionary/uid
package uid

//go:generate go run ../../cmd/dicomuidgen -input ../../internal/standard/uids.tsv -output std_gen.go
