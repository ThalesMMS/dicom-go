package dicomtags

import "github.com/ThalesMMS/dicom-go/core"

var (
	MediaStorageSOPClassUID    = core.NewTag(0x0002, 0x0002)
	MediaStorageSOPInstanceUID = core.NewTag(0x0002, 0x0003)
	TransferSyntaxUID          = core.NewTag(0x0002, 0x0010)
	SOPClassUID                = core.NewTag(0x0008, 0x0016)
	SOPInstanceUID             = core.NewTag(0x0008, 0x0018)
)
