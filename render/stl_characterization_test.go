package render

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func Test_WriteBinarySTL_exports_single_voxel_iso_surface(t *testing.T) {
	// Given
	vol, err := BuildVolume(singleBrightVoxelStack(3, 1, 1, 1))
	if err != nil {
		t.Fatal(err)
	}

	// When
	tris, err := vol.ISOSurfaceTriangles(128)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = WriteBinarySTL(&buf, vol, 128)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(tris) != 12 {
		t.Fatalf("ISOSurfaceTriangles() count = %d, want 12 for one exposed voxel", len(tris))
	}
	if got, want := binary.LittleEndian.Uint32(buf.Bytes()[80:84]), uint32(12); got != want {
		t.Fatalf("binary STL triangle count = %d, want %d", got, want)
	}
	if got, want := buf.Len(), 84+12*50; got != want {
		t.Fatalf("binary STL length = %d, want %d", got, want)
	}
}

func TestWriteBinarySTLPreservesSamplerFailure(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(2, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := vol.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := vol.ISOSurfaceTriangles(1); !errors.Is(err, ErrVolumeStoreClosed) {
		t.Fatalf("ISOSurfaceTriangles() error = %v, want ErrVolumeStoreClosed", err)
	}
	err = WriteBinarySTL(&bytes.Buffer{}, vol, 1)
	if !errors.Is(err, ErrVolumeStoreClosed) {
		t.Fatalf("WriteBinarySTL() error = %v, want ErrVolumeStoreClosed", err)
	}
}
