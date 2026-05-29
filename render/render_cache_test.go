package render

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/display"
)

func Test_RenderCache_reuses_windowed_image_when_frame_and_window_match(t *testing.T) {
	// Given
	frame := renderCacheTestFrame([]byte{64, 128})
	cache := NewRenderCache(1 << 20)
	baseDecode := cache.decodeFrame
	decodeCalls := 0
	cache.decodeFrame = func(frame *Frame) (*decodedFrame, error) {
		decodeCalls++
		return baseDecode(frame)
	}

	// When
	first, err := cache.RenderFrame(frame, WindowLevel{Center: 128, Width: 256})
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.RenderFrame(frame, WindowLevel{Center: 128, Width: 256})
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if decodeCalls != 1 {
		t.Fatalf("decode calls = %d, want 1", decodeCalls)
	}
	if first != second {
		t.Fatal("repeated render returned a different image, want window-cache hit")
	}
}

func Test_RenderCache_distinguishesVOIFunction(t *testing.T) {
	frame := renderCacheTestFrame([]byte{25})
	cache := NewRenderCache(1 << 20)

	linear, err := cache.RenderFrame(frame, WindowLevel{Center: 50, Width: 100})
	if err != nil {
		t.Fatal(err)
	}
	sigmoid, err := cache.RenderFrame(frame, WindowLevel{Center: 50, Width: 100, Function: display.VOISigmoid})
	if err != nil {
		t.Fatal(err)
	}
	if linear == sigmoid {
		t.Fatal("VOI function change returned the cached LINEAR image")
	}
	if gotLinear, gotSigmoid := grayAt(linear, 0, 0), grayAt(sigmoid, 0, 0); gotLinear == gotSigmoid {
		t.Fatalf("VOI function did not change rendered pixel: LINEAR=%d SIGMOID=%d", gotLinear, gotSigmoid)
	}
}

func Test_RenderCache_reuses_decoded_frame_when_only_window_changes(t *testing.T) {
	// Given
	frame := renderCacheTestFrame([]byte{64, 128})
	cache := NewRenderCache(1 << 20)
	baseDecode := cache.decodeFrame
	decodeCalls := 0
	cache.decodeFrame = func(frame *Frame) (*decodedFrame, error) {
		decodeCalls++
		return baseDecode(frame)
	}

	// When
	wide, err := cache.RenderFrame(frame, WindowLevel{Center: 128, Width: 256})
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := cache.RenderFrame(frame, WindowLevel{Center: 64, Width: 128})
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if decodeCalls != 1 {
		t.Fatalf("decode calls = %d, want 1", decodeCalls)
	}
	if wide == narrow {
		t.Fatal("window change returned the same image, want a new windowed image")
	}
	if renderCacheGrayAt(wide, 1, 0) == renderCacheGrayAt(narrow, 1, 0) {
		t.Fatalf("windowed pixel did not change: wide=%d narrow=%d", renderCacheGrayAt(wide, 1, 0), renderCacheGrayAt(narrow, 1, 0))
	}
}

func Test_RenderCache_evicts_decoded_frames_by_byte_budget(t *testing.T) {
	// Given
	firstFrame := renderCacheTestFrame([]byte{64, 128})
	secondFrame := renderCacheTestFrame([]byte{32, 160})
	cache := NewRenderCache(12)
	baseDecode := cache.decodeFrame
	decodeCalls := 0
	cache.decodeFrame = func(frame *Frame) (*decodedFrame, error) {
		decodeCalls++
		return baseDecode(frame)
	}

	// When
	if _, err := cache.RenderFrame(firstFrame, WindowLevel{Center: 128, Width: 256}); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.RenderFrame(secondFrame, WindowLevel{Center: 128, Width: 256}); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.RenderFrame(firstFrame, WindowLevel{Center: 64, Width: 128}); err != nil {
		t.Fatal(err)
	}

	// Then
	if decodeCalls != 3 {
		t.Fatalf("decode calls = %d, want 3 after decoded-frame eviction", decodeCalls)
	}
}

func Test_RenderCache_pressure_keeps_pinned_frame(t *testing.T) {
	first := renderCacheTestFrame([]byte{64, 128})
	second := renderCacheTestFrame([]byte{32, 160})
	window := WindowLevel{Center: 128, Width: 256}
	cache := NewRenderCache(1 << 20)
	if _, err := cache.RenderFrame(first, window); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.RenderFrame(second, window); err != nil {
		t.Fatal(err)
	}
	cache.SetPinnedFrames([]PinnedFrame{{Frame: second, Window: window}})
	if freed := cache.EvictNonPinned(); freed <= 0 {
		t.Fatalf("freed bytes = %d, want non-pinned residency released", freed)
	}
	if cache.ContainsFrame(first, window) {
		t.Fatal("non-pinned frame survived pressure eviction")
	}
	if !cache.ContainsFrame(second, window) {
		t.Fatal("pinned frame was evicted")
	}
	stats := cache.Stats()
	if stats.PinnedBytes <= 0 || stats.DecodedBytes+stats.WindowBytes != stats.PinnedBytes {
		t.Fatalf("stats = %+v, want only pinned residency", stats)
	}
}

func Test_RenderCache_reports_hits_misses_and_evictions(t *testing.T) {
	cache := NewRenderCache(1 << 20)
	frame := renderCacheTestFrame([]byte{64, 128})
	window := WindowLevel{Center: 128, Width: 256}
	if _, err := cache.RenderFrame(frame, window); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.RenderFrame(frame, window); err != nil {
		t.Fatal(err)
	}
	cache.EvictNonPinned()
	stats := cache.Stats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("hits/misses = %d/%d, want 1/1", stats.Hits, stats.Misses)
	}
	if stats.Evictions == 0 {
		t.Fatal("pressure eviction was not counted")
	}
}

func Test_RenderCache_accounts_for_one_oversized_pinned_frame(t *testing.T) {
	frame := renderCacheTestFrame([]byte{64, 128})
	window := WindowLevel{Center: 128, Width: 256}
	cache := NewRenderCache(1)
	cache.SetPinnedFrames([]PinnedFrame{{Frame: frame, Window: window}})

	if _, err := cache.RenderFrame(frame, window); err != nil {
		t.Fatal(err)
	}
	stats := cache.Stats()
	if stats.DecodedBytes+stats.WindowBytes <= 1 {
		t.Fatalf("stats = %+v, want oversized active residency accounted", stats)
	}
	if stats.PinnedBytes != stats.DecodedBytes+stats.WindowBytes {
		t.Fatalf("stats = %+v, want all oversized residency pinned", stats)
	}
	if freed := cache.EvictNonPinned(); freed != 0 {
		t.Fatalf("freed pinned bytes = %d, want 0", freed)
	}

	cache.SetPinnedFrames(nil)
	stats = cache.Stats()
	if stats.DecodedBytes+stats.WindowBytes != 0 {
		t.Fatalf("stats after unpin = %+v, want oversized residency released", stats)
	}
}

func renderCacheTestFrame(data []byte) *Frame {
	return &Frame{
		Metadata: pixeldata.Metadata{
			Rows:                      1,
			Columns:                   uint16(len(data)),
			SamplesPerPixel:           1,
			BitsAllocated:             8,
			BitsStored:                8,
			HighBit:                   7,
			PhotometricInterpretation: "MONOCHROME2",
			PixelRepresentation:       0,
		},
		ByteOrder:     binary.LittleEndian,
		PixelBytes:    data,
		DefaultWindow: WindowLevel{Center: 128, Width: 256},
		Rescale:       Rescale{Slope: 1},
	}
}

func renderCacheGrayAt(img image.Image, x, y int) uint8 {
	return color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y
}
