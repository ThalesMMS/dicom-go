// Package builtin composes the pure-Go pixel decoders shipped with dicom-go.
// Optional product-profile codecs are registered by callers on top of this
// baseline.
package builtin

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeg"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeglossless"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpegls"
	"github.com/ThalesMMS/dicom-go/pixeldata/rle"
)

type codecFamily struct {
	name               string
	transferSyntaxUIDs []string
	register           func(pixeldata.Registry) error
}

var codecFamilies = []codecFamily{
	{
		name:               "JPEG Baseline / Extended",
		transferSyntaxUIDs: []string{jpeg.UID, jpeg.UIDExtended},
		register:           jpeg.Register,
	},
	{
		name:               "RLE",
		transferSyntaxUIDs: []string{rle.UID},
		register:           rle.Register,
	},
	{
		name:               "JPEG Lossless",
		transferSyntaxUIDs: []string{jpeglossless.UIDProcess14, jpeglossless.UIDProcess14SV1},
		register:           jpeglossless.Register,
	},
	{
		name:               "JPEG-LS Lossless",
		transferSyntaxUIDs: []string{jpegls.UID},
		register:           jpegls.Register,
	},
	{
		name:               "JPEG-LS Near-Lossless",
		transferSyntaxUIDs: []string{jpegls.NearLosslessUID},
		register:           jpegls.RegisterNearLossless,
	},
}

// Register adds every built-in decoder to registry in stable family order.
func Register(registry pixeldata.Registry) error {
	if registry == nil {
		return fmt.Errorf("register %s codec: %w", codecFamilies[0].name, pixeldata.ErrCodecRegistryNil)
	}
	for _, family := range codecFamilies {
		if err := family.register(registry); err != nil {
			return fmt.Errorf("register %s codec: %w", family.name, err)
		}
	}
	return nil
}

// NewRegistry returns a fresh caller-owned registry containing all built-in
// decoders. Each call returns an independently synchronized registry.
func NewRegistry() (*pixeldata.MemoryRegistry, error) {
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		return nil, err
	}
	return registry, nil
}

// RegisterDefault adds every built-in decoder to pixeldata.DefaultRegistry.
// It exists for compatibility paths that still use the package-level registry;
// new workflows should prefer NewRegistry.
func RegisterDefault() error {
	return Register(pixeldata.DefaultRegistry)
}

// TransferSyntaxUIDs returns the built-in decoder matrix in registration order.
func TransferSyntaxUIDs() []string {
	var uids []string
	for _, family := range codecFamilies {
		uids = append(uids, family.transferSyntaxUIDs...)
	}
	return uids
}
