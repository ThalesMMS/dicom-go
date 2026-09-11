package storetranscode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeg"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpegls"
	"github.com/ThalesMMS/dicom-go/pixeldata/rle"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func testFile() *object.File {
	o := codecfixture.NativeMultiFrame().Object()
	o.Put(core.Element{Header: core.ElementHeader{Tag: tags.SOPClassUID, VR: core.VRUI}, Value: core.StringValue{"1.2.840.10008.5.1.4.1.1.7"}})
	o.Put(core.Element{Header: core.ElementHeader{Tag: tags.SOPInstanceUID, VR: core.VRUI}, Value: core.StringValue{"1.2.826.0.1.3680043.10.543.916.1"}})
	return &object.File{Dataset: o, TransferSyntax: transfer.ExplicitVRLittleEndian}
}
func testOptions(t testing.TB, target transfer.Syntax) Options {
	t.Helper()
	dec := pixeldata.NewMemoryRegistry()
	enc := pixeldata.NewMemoryEncoderRegistry()
	for _, err := range []error{rle.Register(dec), jpegls.Register(dec), rle.RegisterEncoder(enc), jpegls.RegisterEncoder(enc)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return Options{Target: target, SpoolDirectory: t.TempDir(), Transcode: pixeldata.TranscodeOptions{DecoderRegistry: dec, EncoderRegistry: enc}}
}
func assertEmpty(t testing.TB, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary resources retained: %d %v", len(entries), err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStoreTranscodeWriteFailureRedactsAndPreservesCause(t *testing.T) {
	opts := testOptions(t, transfer.RLELossless)
	source, err := NewSource(dimse.NewFileStoreSource(testFile(), 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	cause := errors.New("SECRET destination details")
	err = opened.WriteDataSet(context.Background(), failingWriter{cause}, opts.Target)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("write cause or redaction lost: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, opts.SpoolDirectory)
}
func encodeSource(t testing.TB, opened dimse.OpenedStoreSource, syntax transfer.Syntax) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := opened.WriteDataSet(context.Background(), &b, syntax); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func compareDataSet(t testing.TB, a, b *object.Object) {
	t.Helper()
	x, err := dicomtest.SemanticDataSet(a.ToDataSet(), a.ValueByteOrder())
	if err != nil {
		t.Fatal(err)
	}
	y, err := dicomtest.SemanticDataSet(b.ToDataSet(), b.ValueByteOrder())
	if err != nil {
		t.Fatal(err)
	}
	if diff := dicomtest.DiffSemantic(x, y); diff != "" {
		t.Fatal(diff)
	}
}

func TestStoreTranscodeVerifiedLosslessAndOriginalFallback(t *testing.T) {
	for _, target := range []transfer.Syntax{transfer.RLELossless, transfer.JPEGLSLossless, transfer.ImplicitVRLittleEndian} {
		t.Run(target.UID, func(t *testing.T) {
			file := testFile()
			opts := testOptions(t, target)
			opts.FallbackOriginal = true
			s, err := NewSource(dimse.NewFileStoreSource(file, 0), opts)
			if err != nil {
				t.Fatal(err)
			}
			d, err := s.Inspect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			assertEmpty(t, opts.SpoolDirectory)
			if len(d.WritableTransferSyntaxUIDs) != 2 || d.WritableTransferSyntaxUIDs[0] != target.UID || d.WritableTransferSyntaxUIDs[1] != file.TransferSyntax.UID || d.Size == 0 {
				t.Fatalf("offer: %+v report=%+v", d, s.Report())
			}
			d.WritableTransferSyntaxUIDs[0] = "corrupt caller snapshot"
			opened, err := s.Open(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			wire := encodeSource(t, opened, target)
			got, err := object.ReadDataSetWithOptions(bytes.NewReader(wire), target, object.ReadFileOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer got.Close()
			if target.Encapsulated {
				got, _, err = pixeldata.TranscodeDataSet(context.Background(), got, target, file.TransferSyntax, opts.Transcode)
				if err != nil {
					t.Fatal(err)
				}
			}
			wantDataSet := file.Dataset
			if !target.ExplicitVR {
				// Implicit VR has normative OW Pixel Data, including 8-bit
				// samples originally represented as explicit OB.
				wantDataSet = object.FromDataSet(file.Dataset.ToDataSet(), nil)
				pixel, _ := wantDataSet.Get(core.TagPixelData)
				pixel.Header.VR = core.VROW
				wantDataSet.Put(pixel)
			}
			compareDataSet(t, got, wantDataSet)
			original := encodeSource(t, opened, file.TransferSyntax)
			var want bytes.Buffer
			if err := object.WriteDataSet(&want, file.Dataset, file.TransferSyntax); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, want.Bytes()) {
				t.Fatal("original fallback reencoded pixels or bytes")
			}
			if err := opened.WriteDataSet(context.Background(), io.Discard, transfer.ExplicitVRBigEndian); !errors.Is(err, dimse.ErrStoreTransferSyntax) {
				t.Fatal("unadvertised syntax written")
			}
			if err := opened.Close(); err != nil {
				t.Fatal(err)
			}
			if err := opened.Close(); err != nil {
				t.Fatal(err)
			}
			assertEmpty(t, opts.SpoolDirectory)
		})
	}
}

func TestStoreTranscodeOpaquePassThroughNeedsNoCodec(t *testing.T) {
	file := testFile()
	opts := testOptions(t, transfer.RLELossless)
	compressed, _, err := pixeldata.TranscodeDataSet(context.Background(), file.Dataset, file.TransferSyntax, opts.Target, opts.Transcode)
	if err != nil {
		t.Fatal(err)
	}
	file = &object.File{Dataset: compressed, TransferSyntax: opts.Target}
	opts.Transcode = pixeldata.TranscodeOptions{}
	s, err := NewSource(dimse.NewFileStoreSource(file, 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := s.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	if err := object.WriteDataSet(&expected, compressed, file.TransferSyntax); err != nil {
		t.Fatal(err)
	}
	if got := encodeSource(t, opened, file.TransferSyntax); !bytes.Equal(got, expected.Bytes()) {
		t.Fatal("opaque pass-through changed bytes")
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, opts.SpoolDirectory)
	r := s.Report()
	if len(r.Candidates) != 1 || r.Candidates[0].Operation != "pass-through" {
		t.Fatalf("report: %+v", r)
	}
}

func TestStoreTranscodeUnavailableCodecAndChangedSourceFailBeforeWrite(t *testing.T) {
	file := testFile()
	opts := Options{Target: transfer.RLELossless, SpoolDirectory: t.TempDir()}
	s, err := NewSource(dimse.NewFileStoreSource(file, 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(context.Background()); !errors.Is(err, pixeldata.ErrEncoderRegistryNil) || !errors.Is(err, ErrNoRepresentation) {
		t.Fatalf("false codec offer: %v cause=%v", err, errors.Unwrap(err))
	}
	assertEmpty(t, opts.SpoolDirectory)
	r := s.Report()
	if len(r.Candidates) != 1 || r.Candidates[0].Producible || !r.Candidates[0].RuntimeMissing || r.Candidates[0].Reason != "encoder-unavailable" {
		t.Fatalf("missing reason: %+v", r)
	}
	// A pixel changes while identity/size/headers remain equal.
	opts = testOptions(t, transfer.RLELossless)
	s, err = NewSource(dimse.NewFileStoreSource(file, 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(context.Background()); err != nil {
		t.Fatal(err)
	}
	e, _ := file.Dataset.Get(core.TagPixelData)
	raw, _ := e.RawBytes()
	changed := append([]byte(nil), raw...)
	changed[0] ^= 1
	file.Dataset.Put(core.NewRawElement(core.TagPixelData, e.VR(), changed))
	if _, err := s.Open(context.Background()); !errors.Is(err, dimse.ErrStoreSourceChanged) {
		t.Fatalf("source mutation accepted: %v", err)
	}
	assertEmpty(t, opts.SpoolDirectory)
}

func TestStoreTranscodeFailureCleanupAndLimits(t *testing.T) {
	for _, kind := range []string{"spool", "input", "cancel", "close", "ignored-write-error"} {
		t.Run(kind, func(t *testing.T) {
			opts := testOptions(t, transfer.RLELossless)
			base := &countedSource{base: dimse.NewFileStoreSource(testFile(), 0)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "spool":
				opts.MaxSpoolBytes = 16
			case "input":
				opts.Transcode.Limits.MaxInputBytes = 16
			case "cancel":
				base.cancel = cancel
			case "close":
				base.closeErr = errors.New("sensitive/path/SECRET")
			case "ignored-write-error":
				opts.MaxSpoolBytes = 16
				base.ignoreWriteError = true
			}
			s, err := NewSource(base, opts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Inspect(ctx)
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("failure unreported/unredacted: %v", err)
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if base.opens != 1 || base.closes != 1 {
				t.Fatalf("source ownership: opens=%d closes=%d", base.opens, base.closes)
			}
			assertEmpty(t, opts.SpoolDirectory)
		})
	}
}

type countedSource struct {
	base             dimse.StoreSource
	opens, closes    int
	cancel           context.CancelFunc
	closeErr         error
	ignoreWriteError bool
}

func (s *countedSource) Inspect(ctx context.Context) (dimse.StoreDescriptor, error) {
	return s.base.Inspect(ctx)
}
func (s *countedSource) Open(ctx context.Context) (dimse.OpenedStoreSource, error) {
	o, err := s.base.Open(ctx)
	if err != nil {
		return o, err
	}
	s.opens++
	write, close := o.WriteDataSet, o.Close
	o.WriteDataSet = func(ctx context.Context, w io.Writer, syntax transfer.Syntax) error {
		if s.cancel != nil {
			s.cancel()
		}
		err := write(ctx, w, syntax)
		if s.ignoreWriteError {
			return nil
		}
		return err
	}
	o.Close = func() error { s.closes++; return errors.Join(close(), s.closeErr) }
	return o, nil
}

func TestStoreTranscodeConcurrentIndependentHandles(t *testing.T) {
	opts := testOptions(t, transfer.RLELossless)
	s, err := NewSource(dimse.NewFileStoreSource(testFile(), 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	// Warm the borrowed Object facade's caches before concurrent reads: callers
	// must provide a concurrent-safe base when they share one source.
	if _, err := s.Inspect(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := s.Open(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			if err := o.WriteDataSet(context.Background(), io.Discard, opts.Target); err != nil {
				t.Error(err)
			}
			if err := o.Close(); err != nil {
				t.Error(err)
			}
			_ = s.Report()
		}()
	}
	wg.Wait()
	assertEmpty(t, opts.SpoolDirectory)
}

func TestStoreTranscodeLossyRequiresAuthorizationAndStableDerivedIdentity(t *testing.T) {
	file := testFile()
	before, _ := file.Dataset.GetUID(tags.SOPInstanceUID)
	opts := testOptions(t, transfer.JPEGBaseline)
	if err := jpeg.RegisterEncoder(opts.Transcode.EncoderRegistry, 95); err != nil {
		t.Fatal(err)
	}
	s, err := NewSource(dimse.NewFileStoreSource(file, 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(context.Background()); !errors.Is(err, pixeldata.ErrTranscodeLossyDisallowed) {
		t.Fatalf("lossy output not gated: %v", err)
	}
	assertEmpty(t, opts.SpoolDirectory)
	opts.Transcode.AllowLossy = true
	s, err = NewSource(dimse.NewFileStoreSource(file, 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Inspect(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v cause=%v", err, errors.Unwrap(err))
	}
	if d.SOPInstanceUID == before || !core.IsValidUID(d.SOPInstanceUID) || len(d.WritableTransferSyntaxUIDs) != 1 {
		t.Fatal("lossy identity or policy")
	}
	for range 2 {
		o, err := s.Open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if o.Descriptor.SOPInstanceUID != d.SOPInstanceUID {
			t.Fatal("command identity changed between inspect/open")
		}
		wire := encodeSource(t, o, opts.Target)
		if err := o.Close(); err != nil {
			t.Fatal(err)
		}
		parsed, err := object.ReadDataSetWithOptions(bytes.NewReader(wire), opts.Target, object.ReadFileOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := parsed.GetUID(tags.SOPInstanceUID)
		lossy, _ := parsed.GetString(core.NewTag(0x28, 0x2110))
		if got != d.SOPInstanceUID || lossy != "01" {
			t.Fatal("dataset identity/lossy history differs from plan")
		}
		_ = parsed.Close()
	}
	if got, _ := file.Dataset.GetUID(tags.SOPInstanceUID); got != before {
		t.Fatal("lossy preparation mutated origin")
	}
	assertEmpty(t, opts.SpoolDirectory)
	opts.FallbackOriginal = true
	if _, err := NewSource(dimse.NewFileStoreSource(file, 0), opts); !errors.Is(err, ErrOptions) {
		t.Fatal("mixed identities allowed")
	}
}

func TestStoreTranscodeZeroPolicyDelegatesUnchanged(t *testing.T) {
	base := &countedSource{base: dimse.NewFileStoreSource(testFile(), 0)}
	s, err := NewSource(base, Options{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Inspect(context.Background())
	if err != nil || base.opens != 0 {
		t.Fatal("default preflight transformed or opened source")
	}
	want, _ := base.Inspect(context.Background())
	if !sameDescriptor(d, want) {
		t.Fatal("default offers changed")
	}
	o, err := s.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if base.opens != 1 || base.closes != 1 {
		t.Fatal("default ownership changed")
	}
}

func TestStoreTranscodeRuntimeRevalidatedAndFallbackExplicit(t *testing.T) {
	opts := testOptions(t, transfer.RLELossless)
	toggle := &runtimeEncoder{}
	registry := pixeldata.NewMemoryEncoderRegistry()
	if err := registry.RegisterEncoder(opts.Target.UID, toggle); err != nil {
		t.Fatal(err)
	}
	opts.Transcode.EncoderRegistry = registry
	s, err := NewSource(dimse.NewFileStoreSource(testFile(), 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(context.Background()); err != nil {
		t.Fatal(err)
	}
	toggle.missing.Store(true)
	if _, err := s.Open(context.Background()); !errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("runtime change not rejected/redacted: %v", err)
	}
	assertEmpty(t, opts.SpoolDirectory)
	r := s.Report()
	if len(r.Candidates) != 1 || r.Candidates[0].Producible || r.Candidates[0].Reason != "runtime-unavailable" {
		t.Fatalf("runtime diagnostic: %+v", r)
	}
	opts.FallbackOriginal = true
	s, err = NewSource(dimse.NewFileStoreSource(testFile(), 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Inspect(context.Background())
	if err != nil || len(d.WritableTransferSyntaxUIDs) != 1 || d.TransferSyntaxUID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("explicit original fallback: %+v %v", d, err)
	}
	o, err := s.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = encodeSource(t, o, transfer.ExplicitVRLittleEndian)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, opts.SpoolDirectory)
}

type runtimeEncoder struct{ missing atomic.Bool }

func (*runtimeEncoder) Capabilities() pixeldata.EncoderCapabilities {
	return rle.NewEncoder().Capabilities()
}
func (e *runtimeEncoder) EncodeFrame(ctx context.Context, frame []byte, metadata pixeldata.Metadata) (pixeldata.EncodedFrame, error) {
	if e.missing.Load() {
		return pixeldata.EncodedFrame{}, fmt.Errorf("SECRET backend path: %w", os.ErrNotExist)
	}
	return rle.NewEncoder().EncodeFrame(ctx, frame, metadata)
}

func TestStoreTranscodeUnsupportedProfileHasNoOffer(t *testing.T) {
	file := testFile()
	for _, p := range []struct {
		tag   core.Tag
		value uint16
	}{{tags.BitsAllocated, 16}, {tags.BitsStored, 16}, {tags.HighBit, 15}} {
		file.Dataset.Put(core.Element{Header: core.ElementHeader{Tag: p.tag, VR: core.VRUS}, Value: core.Uint16Value{p.value}})
	}
	file.Dataset.Put(core.NewRawElement(core.TagPixelData, core.VROW, make([]byte, 16)))
	opts := testOptions(t, transfer.JPEGBaseline)
	opts.Transcode.AllowLossy = true
	if err := jpeg.RegisterEncoder(opts.Transcode.EncoderRegistry, 90); err != nil {
		t.Fatal(err)
	}
	s, err := NewSource(dimse.NewFileStoreSource(file, 0), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(context.Background()); !errors.Is(err, pixeldata.ErrUnsupportedEncoderMetadata) {
		t.Fatalf("16-bit baseline advertised: %v", err)
	}
	r := s.Report()
	if len(r.Candidates) != 1 || r.Candidates[0].Producible || r.Candidates[0].Reason != "profile-unsupported" {
		t.Fatalf("profile diagnostic: %+v", r)
	}
	assertEmpty(t, opts.SpoolDirectory)
}
