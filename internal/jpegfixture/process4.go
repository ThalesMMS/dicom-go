// Package jpegfixture holds shared independently generated synthetic JPEG vectors.
package jpegfixture

// Process4Base64 is an independently generated 12-bit sequential-DCT
// JPEG interchange stream. It was generated from SourceSamples with:
//
//	libjpeg-turbo 3.1.0
//	cjpeg -precision 12 -quality 95 -optimize -grayscale input.pgm
//
// The JPEG SHA-256 is
// 2405e5ba4b802c6e97db2da1b0023fbac9e1957ad0e79b30b3c6597d451e6327.
// The expected samples below are the output of libjpeg-turbo 3.1.0 djpeg
// -strict -pnm, not output produced by this package.
const Process4Base64 = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAIBAQEBAQIBAQECAgICAgQDAgICAgUEBAMEBgUGBgYFBgYGBwkIBgcJBwYGCAsICQoKCgoKBggLDAsKDAkKCgr/wQALDAAIAAgBAREA/8QAFAABAAAAAAAAAAAAAAAAAAAAC//EAB8QAAEDBAMBAAAAAAAAAAAAAAYHCAoEBQkLAgMMDf/aAAgBAQAAPwAF75ADFd0uO/A227V9fN66DzOMe5U5qepGc4/azLXjEiUw3liim5i2VOER+soaFwCNvVZ5hbwtcHFZJnNA97QOsBB9C1e6DZVh867VGE02HrYRGRbYC3//2Q=="

func SourceSamples() []uint16 {
	return []uint16{
		0, 1, 15, 16, 255, 256, 1023, 2047,
		2048, 3071, 3840, 4095, 17, 257, 1024, 2049,
		33, 511, 1000, 1500, 2000, 2500, 3000, 3500,
		7, 77, 777, 1777, 2777, 3777, 4000, 4094,
		64, 128, 512, 768, 1280, 1536, 2304, 2816,
		320, 640, 960, 1600, 2240, 2880, 3520, 3968,
		5, 25, 125, 625, 1125, 2125, 3125, 412,
		4093, 3, 31, 310, 1310, 2310, 3310, 4010,
	}
}

func ReferenceSamples() []uint16 {
	return []uint16{
		0, 0, 14, 15, 256, 254, 1026, 2046,
		2050, 3071, 3842, 4095, 16, 257, 1025, 2049,
		30, 512, 999, 1497, 2003, 2499, 2998, 3502,
		7, 78, 779, 1777, 2776, 3778, 4000, 4093,
		65, 122, 515, 767, 1279, 1537, 2304, 2818,
		319, 644, 959, 1600, 2238, 2881, 3520, 3968,
		4, 23, 126, 627, 1126, 2123, 3125, 414,
		4095, 0, 33, 309, 1308, 2311, 3311, 4009,
	}
}
