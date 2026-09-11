package codecfixture

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata/rle"
)

func TestCodecConformanceBaseline(t *testing.T) {
	for _, tc := range []Case{
		NativeSmall(),
		NativeMultiFrame(),
		RLELosslessSmall(),
		JPEGBaselineSmall(),
		JPEGExtendedSmall(),
		JPEGExtendedProcess4Mono12(),
		JPEGLosslessSmall(),
		JPEGLosslessSV1RGB8Interleaved(),
	} {
		t.Run(tc.Name, func(t *testing.T) {
			if !tc.Provenance.Synthetic || !tc.Provenance.NoPHI {
				t.Fatalf("provenance = %#v, want synthetic no-PHI fixture", tc.Provenance)
			}
			registry, err := tc.Registry()
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCase(registry, tc); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCaseObjectDetachesTypedNumericValues(t *testing.T) {
	c := NativeSmall()
	c.Elements = append(c.Elements, core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0011, 0x1001), VR: core.VRUS}, Value: core.Uint16Value{7, 9}})
	first, second := c.Object(), c.Object()
	elem, _ := first.Get(core.NewTag(0x0011, 0x1001))
	elem.Value.(core.Uint16Value)[0] = 123
	other, _ := second.Get(core.NewTag(0x0011, 0x1001))
	if other.Value.(core.Uint16Value)[0] != 7 {
		t.Fatal("independent fixture objects share typed numeric storage")
	}
}

func TestCodecConformanceClassifiesExpectedErrors(t *testing.T) {
	for _, tc := range []Case{
		UnsupportedMetadataJPEGExtended(),
		MalformedJPEGExtended(),
		MetadataMismatchRLE(),
		MissingJPEGXLAdapter(),
		DependencyUnavailableJPEGLS(),
		DependencyUnavailableJPEGXL(),
	} {
		t.Run(tc.Name, func(t *testing.T) {
			registry, err := tc.Registry()
			if err != nil {
				t.Fatal(err)
			}
			result := RunCase(registry, tc)
			if result.Kind != tc.ExpectedError {
				t.Fatalf("RunCase() kind = %s, want %s (err=%v)", result.Kind, tc.ExpectedError, result.Err)
			}
			if err := ValidateCase(registry, tc); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJPEGXLRegisteredCodecBoundaryRejectsUnavailableDependency(t *testing.T) {
	tc := DependencyUnavailableJPEGXL()
	registry, err := tc.Registry()
	if err != nil {
		t.Fatal(err)
	}

	result := RunCase(registry, tc)
	if result.Kind != ErrorDependencyUnavailable {
		t.Fatalf("RunCase() kind = %s, want %s (err=%v)", result.Kind, ErrorDependencyUnavailable, result.Err)
	}
	if !errors.Is(result.Err, ErrDependencyUnavailable) {
		t.Fatalf("RunCase() error = %v, want ErrDependencyUnavailable", result.Err)
	}
	if err := ValidateCase(registry, tc); err != nil {
		t.Fatal(err)
	}
}

func TestCodecConformanceClassifiesDecodeMismatch(t *testing.T) {
	tc := NativeSmall()
	tc.ExpectedFrames[0] = []byte{255, 255, 255, 255}
	registry, err := tc.Registry()
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateCase(registry, tc)
	if !errors.Is(err, ErrDecodeMismatch) {
		t.Fatalf("ValidateCase() error = %v, want ErrDecodeMismatch", err)
	}
	if got := ClassifyError(err); got != ErrorDecodeMismatch {
		t.Fatalf("ClassifyError() = %s, want %s", got, ErrorDecodeMismatch)
	}
}

func TestCodecConformanceClassifiesUnknownErrors(t *testing.T) {
	err := errors.New("codecfixture test: unexpected adapter failure")
	if got := ClassifyError(err); got != ErrorUnknown {
		t.Fatalf("ClassifyError() = %s, want %s", got, ErrorUnknown)
	}
}

func TestCodecConformanceClassifiesRLEInvalidSegmentCount(t *testing.T) {
	if got := ClassifyError(rle.ErrInvalidSegmentCount); got != ErrorMalformedStream {
		t.Fatalf("ClassifyError() = %s, want %s", got, ErrorMalformedStream)
	}
}

func TestCodecFixtureUIDsAreUniquePerCase(t *testing.T) {
	cases := []Case{
		NativeSmall(),
		RLELosslessSmall(),
		JPEGBaselineSmall(),
		JPEGExtendedSmall(),
		JPEGLosslessSmall(),
		JPEGLosslessSV1RGB8Interleaved(),
		MalformedJPEGExtended(),
		UnsupportedMetadataJPEGExtended(),
		MetadataMismatchRLE(),
		MissingJPEGXLAdapter(),
		DependencyUnavailableJPEGLS(),
		DependencyUnavailableJPEGXL(),
		JPEG2000LosslessSmall([]byte{0xff, 0x4f}, []byte{0, 64, 128, 255}),
		HTJ2KLosslessSmall([]byte{0xff, 0x4f}, []byte{0, 64, 128, 255}),
	}

	for _, tag := range []core.Tag{tagSOPInstanceUID, tagStudyInstanceUID, tagSeriesInstanceUID} {
		seen := map[string]string{}
		for _, tc := range cases {
			uid := stringValueForTag(t, tc, tag)
			if prior, ok := seen[uid]; ok {
				t.Fatalf("tag %s UID %s reused by %s and %s", tag, uid, prior, tc.Name)
			}
			seen[uid] = tc.Name
		}
	}
}

func TestJPEGLosslessSV1RGB8InterleavedProvenance(t *testing.T) {
	tc := JPEGLosslessSV1RGB8Interleaved()
	for _, want := range []string{
		"libjpeg-turbo 3.1.0",
		"cjpeg -precision 8 -lossless 1,0 -rgb -sample 1x1,1x1,1x1",
	} {
		if !strings.Contains(tc.Provenance.Source, want) {
			t.Fatalf("provenance source = %q, want substring %q", tc.Provenance.Source, want)
		}
	}
	if !strings.Contains(tc.Provenance.Notes, "c9643dc57a6e631865084223af411c514d52edc30caaa3fe99eab5c9561d7e68") {
		t.Fatalf("provenance notes = %q, want codestream SHA-256", tc.Provenance.Notes)
	}
	if !tc.Provenance.Synthetic || !tc.Provenance.NoPHI || tc.Provenance.Permission == "" {
		t.Fatalf("provenance = %#v, want redistributable synthetic no-PHI fixture", tc.Provenance)
	}
}

func TestCodecFixtureObjectDeepClonesElementValues(t *testing.T) {
	tc := RLELosslessSmall()
	obj := tc.Object()
	elem, ok := obj.Get(core.TagPixelData)
	if !ok {
		t.Fatal("PixelData element missing")
	}
	seq, ok := elem.Value.(core.FragmentSequence)
	if !ok || len(seq.Fragments) == 0 || len(seq.Fragments[0]) == 0 {
		t.Fatalf("PixelData = %#v, want fragment sequence", elem.Value)
	}

	original := seq.Fragments[0][0]
	seq.Fragments[0][0] ^= 0xff

	freshElem, ok := tc.Object().Get(core.TagPixelData)
	if !ok {
		t.Fatal("fresh PixelData element missing")
	}
	freshSeq := freshElem.Value.(core.FragmentSequence)
	if freshSeq.Fragments[0][0] != original {
		t.Fatalf("fresh fragment first byte = %#x, want original %#x", freshSeq.Fragments[0][0], original)
	}
}

func TestCodecFixturePart10RoundTrip(t *testing.T) {
	tc := NativeSmall()
	data, err := tc.Part10Bytes()
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if file.TransferSyntax.UID != tc.Syntax.UID {
		t.Fatalf("TransferSyntax = %q, want %q", file.TransferSyntax.UID, tc.Syntax.UID)
	}
}

func stringValueForTag(t *testing.T, tc Case, tag core.Tag) string {
	t.Helper()
	elem, ok := tc.Object().Get(tag)
	if !ok {
		t.Fatalf("%s missing from %s", tag, tc.Name)
	}
	value := elem.StringValue()
	if value == "" {
		t.Fatalf("%s for %s is empty", tag, tc.Name)
	}
	return value
}

func BenchmarkDecodeSyntheticCases(b *testing.B) {
	for _, tc := range []Case{NativeSmall(), NativeMedium(), NativeLarge()} {
		b.Run(string(tc.Size), func(b *testing.B) {
			registry, err := tc.Registry()
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := ValidateCase(registry, tc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
