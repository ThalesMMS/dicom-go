package transfer

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

var _ Registry = (*MemoryRegistry)(nil)

func TestMemoryRegistryNormalizesUIDs(t *testing.T) {
	r := NewRegistry(Syntax{
		UID:        "1.2.3 \x00",
		Name:       "Test Syntax",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
	})

	got, ok := r.Get("1.2.3 \x00")
	if !ok {
		t.Fatal("expected normalized UID lookup to succeed")
	}
	if got.UID != "1.2.3" {
		t.Fatalf("unexpected normalized UID: %q", got.UID)
	}
	if all := r.All(); len(all) != 1 || all[0].UID != "1.2.3" {
		t.Fatalf("registry stored unexpected UID set: %#v", all)
	}
}

func TestNormalizeUID(t *testing.T) {
	tests := map[string]string{
		"1.2.3 \x00":     "1.2.3",
		"1.2.3\x00 \x00": "1.2.3",
		" \x00":          "",
		" 1.2.3":         " 1.2.3",
		"1.2\x003 4":     "1.2\x003 4",
	}

	for input, want := range tests {
		if got := NormalizeUID(input); got != want {
			t.Fatalf("NormalizeUID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMemoryRegistryRegisterIgnoresEmptyNormalizedUID(t *testing.T) {
	r := NewRegistry()
	r.Register(Syntax{
		UID:        " \x00",
		Name:       "Invalid Syntax",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
	})

	if got := r.All(); len(got) != 0 {
		t.Fatalf("expected empty registry after invalid registration, got %#v", got)
	}

	if syntax, ok := r.Get(""); ok || syntax != (Syntax{}) {
		t.Fatalf("expected empty UID lookup to fail after invalid registration, got %#v ok=%v", syntax, ok)
	}
}

func TestMemoryRegistryRegisterNilReceiver(t *testing.T) {
	var r Registry = (*MemoryRegistry)(nil)
	r.Register(Syntax{
		UID:        "1.2.3",
		Name:       "Test Syntax",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
	})
	if got, ok := r.Get("1.2.3"); ok || got != (Syntax{}) {
		t.Fatalf("nil registry Get() = (%#v, %t), want zero, false", got, ok)
	}
	if got := r.All(); got != nil {
		t.Fatalf("nil registry All() = %#v, want nil", got)
	}
}

func TestMemoryRegistryAllSortedByUIDAcrossCalls(t *testing.T) {
	r := NewRegistry(
		Syntax{UID: "1.2.840.10008.1.2.4.50"},
		Syntax{UID: "1.2.840.10008.1.2"},
		Syntax{UID: "1.2.840.10008.1.2.1"},
	)

	first := r.All()
	second := r.All()

	want := []string{
		"1.2.840.10008.1.2",
		"1.2.840.10008.1.2.1",
		"1.2.840.10008.1.2.4.50",
	}
	gotFirst := []string{first[0].UID, first[1].UID, first[2].UID}
	gotSecond := []string{second[0].UID, second[1].UID, second[2].UID}

	if !reflect.DeepEqual(gotFirst, want) {
		t.Fatalf("first All() call returned unexpected order: got %v want %v", gotFirst, want)
	}
	if !reflect.DeepEqual(gotSecond, want) {
		t.Fatalf("second All() call returned unexpected order: got %v want %v", gotSecond, want)
	}
	first[0].UID = "mutated"
	if got := r.All()[0].UID; got != want[0] {
		t.Fatalf("mutating All() snapshot changed registry: got %q want %q", got, want[0])
	}
	r.Register(Syntax{UID: want[0], Name: "replacement"})
	r.Register(Syntax{UID: "1.2.840.10008.1.2.5", Name: "new"})
	if len(second) != 3 || second[0].Name != "" {
		t.Fatalf("registration changed prior snapshot: %#v", second)
	}
	if current := r.All(); len(current) != 4 || current[0].Name != "replacement" {
		t.Fatalf("current snapshot did not reflect registrations: %#v", current)
	}
}

func TestMemoryRegistryConcurrentAccess(t *testing.T) {
	registry := NewRegistry()
	const workers = 8
	const registrationsPerWorker = 64
	start := make(chan struct{})
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for index := 0; index < registrationsPerWorker; index++ {
				uid := fmt.Sprintf("1.2.826.0.1.3680043.10.%d.%d", worker, index)
				registry.Register(Syntax{UID: uid, Name: uid})
				if syntax, ok := registry.Get(uid); !ok || syntax.UID != uid {
					t.Errorf("Get(%q) = %#v, %v", uid, syntax, ok)
					return
				}
				_ = registry.All()
			}
		}()
	}
	close(start)
	group.Wait()
	if got, want := len(registry.All()), workers*registrationsPerWorker; got != want {
		t.Fatalf("registry entries = %d, want %d", got, want)
	}
}

type benchmarkRegistry interface {
	Get(string) (Syntax, bool)
	Register(Syntax)
}

type benchmarkMutexRegistry struct {
	mu    sync.Mutex
	byUID map[string]Syntax
}

func (r *benchmarkMutexRegistry) Get(uid string) (Syntax, bool) {
	uid = NormalizeUID(uid)
	r.mu.Lock()
	defer r.mu.Unlock()
	syntax, ok := r.byUID[uid]
	return syntax, ok
}

func (r *benchmarkMutexRegistry) Register(syntax Syntax) {
	syntax.UID = NormalizeUID(syntax.UID)
	r.mu.Lock()
	r.byUID[syntax.UID] = syntax
	r.mu.Unlock()
}

func benchmarkRegistries() []struct {
	name     string
	registry benchmarkRegistry
} {
	return []struct {
		name     string
		registry benchmarkRegistry
	}{
		{name: "rwmutex", registry: NewRegistry()},
		{name: "mutex_baseline", registry: &benchmarkMutexRegistry{byUID: make(map[string]Syntax)}},
	}
}

func seedBenchmarkRegistry(registry benchmarkRegistry) {
	for index := 0; index < 128; index++ {
		uid := fmt.Sprintf("1.2.826.0.1.3680043.10.863.%d", index)
		registry.Register(Syntax{UID: uid, Name: uid})
	}
}

func BenchmarkMemoryRegistryGetReadHeavy(b *testing.B) {
	const uid = "1.2.826.0.1.3680043.10.863.64"
	for _, benchmark := range benchmarkRegistries() {
		b.Run(benchmark.name, func(b *testing.B) {
			seedBenchmarkRegistry(benchmark.registry)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, ok := benchmark.registry.Get(uid); !ok {
						b.Fatal("registered syntax missing")
					}
				}
			})
		})
	}
}

func BenchmarkMemoryRegistryMixedReadWrite(b *testing.B) {
	const uid = "1.2.826.0.1.3680043.10.863.64"
	for _, benchmark := range benchmarkRegistries() {
		b.Run(benchmark.name, func(b *testing.B) {
			seedBenchmarkRegistry(benchmark.registry)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				iteration := 0
				for pb.Next() {
					iteration++
					if iteration%100 == 0 {
						benchmark.registry.Register(Syntax{UID: uid, Name: uid})
						continue
					}
					if _, ok := benchmark.registry.Get(uid); !ok {
						b.Fatal("registered syntax missing")
					}
				}
			})
		})
	}
}

func BenchmarkMemoryRegistryAllSnapshot(b *testing.B) {
	registry := NewRegistry()
	for index := 0; index < 128; index++ {
		uid := fmt.Sprintf("1.2.826.0.1.3680043.10.863.%d", index)
		registry.Register(Syntax{UID: uid, Name: uid})
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if got := registry.All(); len(got) != 128 {
				b.Fatalf("All() entries = %d, want 128", len(got))
			}
		}
	})
}

func TestDefaultRegistryGetReturnsExpectedSyntaxes(t *testing.T) {
	tests := []Syntax{
		ImplicitVRLittleEndian,
		ExplicitVRLittleEndian,
		DeflatedExplicitVRLittleEndian,
		ExplicitVRBigEndian,
		JPEGBaseline,
		JPEGExtended,
		JPEGLosslessNonHierarchical,
		JPEGLosslessSV1,
		JPEGLSLossless,
		JPEGLSNearLossless,
		JPEG2000LosslessOnly,
		JPEG2000,
		JPEG2000Part2Lossless,
		JPEG2000Part2,
		HTJ2KLossless,
		HTJ2KLosslessRPCL,
		HTJ2K,
		RLELossless,
		EncapsulatedUncompressedExplicitVRLittleEndian,
	}

	for _, want := range tests {
		got, ok := DefaultRegistry.Get(want.UID + " \x00")
		if !ok {
			t.Fatalf("expected transfer syntax %q to be registered", want.UID)
		}
		if got.Name != want.Name {
			t.Fatalf("unexpected syntax name for %q: got %q want %q", want.UID, got.Name, want.Name)
		}
		if got.UID != want.UID {
			t.Fatalf("unexpected syntax UID for %q: got %q want %q", want.Name, got.UID, want.UID)
		}
	}
}

func TestDefaultRegistryIncludesSupportedJPEGSyntaxes(t *testing.T) {
	for _, want := range []Syntax{JPEGBaseline, JPEGExtended, JPEGLosslessNonHierarchical, JPEGLosslessSV1} {
		got, ok := DefaultRegistry.Get(want.UID + " \x00")
		if !ok {
			t.Fatalf("expected JPEG transfer syntax %q to be registered", want.UID)
		}
		if got.Name != want.Name {
			t.Fatalf("unexpected syntax name for %q: got %q want %q", want.UID, got.Name, want.Name)
		}
		if !got.Supported || !got.Encapsulated || !got.CodecAvailable || !got.RequiresCodec() {
			t.Fatalf("JPEG transfer syntax flags = %#v, want supported encapsulated codec syntax", got)
		}
	}
}

func TestDefaultRegistryGetUnknownUID(t *testing.T) {
	got, ok := DefaultRegistry.Get("1.2.840.10008.999.999")
	if ok {
		t.Fatalf("expected unknown UID lookup to fail, got %#v", got)
	}
	if got != (Syntax{}) {
		t.Fatalf("expected zero Syntax for unknown UID, got %#v", got)
	}
}

func TestDefaultRegistryIncludesKnownUnsupportedSyntaxes(t *testing.T) {
	tests := []struct {
		uid           string
		wantName      string
		wantSupported bool
	}{
		{
			uid:           "1.2.840.10008.1.2.4.95",
			wantName:      "JPIP Referenced Deflate",
			wantSupported: true,
		},
		{
			uid:           JPIPHTJ2KReferenced.UID,
			wantName:      JPIPHTJ2KReferenced.Name,
			wantSupported: true,
		},
		{
			uid:           "1.2.840.10008.1.2.6.2",
			wantName:      "XML Encoding (Retired)",
			wantSupported: false,
		},
		{
			uid:           SMPTEST211030PCMDigitalAudio.UID,
			wantName:      SMPTEST211030PCMDigitalAudio.Name,
			wantSupported: false,
		},
		{
			uid:           HTJ2KLossless.UID,
			wantName:      HTJ2KLossless.Name,
			wantSupported: true,
		},
		{
			uid:           JPEGXLLossless.UID,
			wantName:      JPEGXLLossless.Name,
			wantSupported: true,
		},
		{
			uid:           JPEGXLJPEGRecompression.UID,
			wantName:      JPEGXLJPEGRecompression.Name,
			wantSupported: true,
		},
		{
			uid:           JPEGXL.UID,
			wantName:      JPEGXL.Name,
			wantSupported: true,
		},
	}

	for _, tt := range tests {
		got, ok := DefaultRegistry.Get(tt.uid + " \x00")
		if !ok {
			t.Fatalf("expected known transfer syntax %q to be registered", tt.uid)
		}
		if got.Name != tt.wantName {
			t.Fatalf("unexpected syntax name for %q: got %q want %q", tt.uid, got.Name, tt.wantName)
		}
		if got.Supported != tt.wantSupported {
			t.Fatalf("unexpected supported flag for %q: got %v want %v", tt.uid, got.Supported, tt.wantSupported)
		}
	}
}

func TestDefaultRegistryIncludesKnownVideoMediaSyntaxes(t *testing.T) {
	for _, want := range []Syntax{MPEG2MPML, MPEG4HP41, HEVCMP51} {
		got, ok := DefaultRegistry.Get(want.UID + " \x00")
		if !ok {
			t.Fatalf("expected video transfer syntax %q to be registered", want.UID)
		}
		if got.Name != want.Name {
			t.Fatalf("unexpected syntax name for %q: got %q want %q", want.UID, got.Name, want.Name)
		}
		if !got.Supported {
			t.Fatalf("expected video transfer syntax %q to be metadata/payload-readable", want.UID)
		}
		if !got.Encapsulated || got.CodecAvailable || got.RequiresCodec() {
			t.Fatalf("video transfer syntax flags = %#v, want encapsulated media without still-image codec", got)
		}
	}
}
