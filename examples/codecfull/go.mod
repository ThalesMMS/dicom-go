module github.com/ThalesMMS/dicom-go/examples/codecfull

go 1.22

require (
	github.com/ThalesMMS/dicom-go v0.0.0
	github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpeg2000 v0.0.0
	github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegls v0.0.0
	github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegxl v0.0.0
)

require (
	github.com/ebitengine/purego v0.10.1 // indirect
	github.com/mrjoshuak/go-jpeg2000 v1.2.1 // indirect
	golang.org/x/text v0.3.8 // indirect
)

replace github.com/ThalesMMS/dicom-go => ../..

replace github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpeg2000 => ../codec-adapters/jpeg2000

replace github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegls => ../codec-adapters/jpegls

replace github.com/ThalesMMS/dicom-go/examples/codec-adapters/jpegxl => ../codec-adapters/jpegxl
