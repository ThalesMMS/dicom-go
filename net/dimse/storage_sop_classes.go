package dimse

import (
	"github.com/ThalesMMS/dicom-go/gsps"
	"github.com/ThalesMMS/dicom-go/rtstruct"
	"github.com/ThalesMMS/dicom-go/seg"
	"github.com/ThalesMMS/dicom-go/sr"
	"github.com/ThalesMMS/dicom-go/vps"
	"github.com/ThalesMMS/dicom-go/waveform"
)

var defaultStorageSOPClassUIDs = append([]string{
	"1.2.840.10008.5.1.4.1.1.1",
	"1.2.840.10008.5.1.4.1.1.2",
	"1.2.840.10008.5.1.4.1.1.2.1",
	"1.2.840.10008.5.1.4.1.1.4",
	"1.2.840.10008.5.1.4.1.1.4.1",
	"1.2.840.10008.5.1.4.1.1.6.1",
	"1.2.840.10008.5.1.4.1.1.7",
	"1.2.840.10008.5.1.4.1.1.7.1",
	"1.2.840.10008.5.1.4.1.1.7.2",
	"1.2.840.10008.5.1.4.1.1.7.3",
	"1.2.840.10008.5.1.4.1.1.7.4",
	"1.2.840.10008.5.1.4.1.1.12.1",
	"1.2.840.10008.5.1.4.1.1.12.2",
	"1.2.840.10008.5.1.4.1.1.20",
	"1.2.840.10008.5.1.4.1.1.128",
	"1.2.840.10008.5.1.4.1.1.1.1",
	"1.2.840.10008.5.1.4.1.1.1.1.1",
	seg.SegmentationStorage,
	seg.LabelMapSegmentationStorage,
	sr.EnhancedSRStorage,
	sr.ComprehensiveSRStorage,
	sr.Comprehensive3DSRStorage,
	gsps.GrayscaleSoftcopyPresentationStateStorage,
	rtstruct.RTStructureSetStorage,
	vps.GrayscalePlanarMPRVolumetricPresentationStateStorage,
	vps.CompositingPlanarMPRVolumetricPresentationStateStorage,
	vps.VolumeRenderingVolumetricPresentationStateStorage,
	vps.SegmentedVolumeRenderingVolumetricPresentationStateStorage,
	vps.MultipleVolumeRenderingVolumetricPresentationStateStorage,
}, waveform.SupportedStorageSOPClassUIDs()...)

// DefaultStorageSOPClassUIDs returns the DICOM Storage SOP Class UIDs accepted
// by the library's default Storage SCP profile.
func DefaultStorageSOPClassUIDs() []string {
	return append([]string(nil), defaultStorageSOPClassUIDs...)
}
