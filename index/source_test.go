package index

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestSourceAdaptersOwnershipAndRedaction(t *testing.T) {
	t.Run("borrowed ReaderAt", func(t *testing.T) {
		source := ReaderAtSource("SECRET/PATIENT.dcm", bytes.NewReader([]byte("abc")), 3)
		opened, err := source.Open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if opened.Info.Origin != "SECRET/PATIENT.dcm" || opened.Info.Size != 3 {
			t.Fatalf("info = %+v", opened.Info)
		}
		if err := opened.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("path error", func(t *testing.T) {
		_, err := PathSource("SECRET-PATIENT-NAME/not-found.dcm").Open(context.Background())
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error = %v", err)
		}
		if strings.Contains(err.Error(), "SECRET-PATIENT-NAME") {
			t.Fatalf("error leaked origin: %v", err)
		}
	})
}

func TestReadSeekerSourceIsReplayableConcurrentSafeAndSizeBounded(t *testing.T) {
	encoded := encodeIndexFixture(t, transfer.ExplicitVRLittleEndian, []core.Element{
		textIndexElement(tags.SOPClassUID, core.VRUI, testSOPClassUID),
		textIndexElement(tags.SOPInstanceUID, core.VRUI, testSOPInstanceUID),
		textIndexElement(tags.StudyInstanceUID, core.VRUI, "1.2.3.replay"),
	})
	reader := bytes.NewReader(encoded)
	source := ReadSeekerSource("", reader, int64(len(encoded)))
	for i := 0; i < 2; i++ {
		result, err := Read(context.Background(), source, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if result.Record.Study.InstanceUID != "1.2.3.replay" {
			t.Fatalf("read %d UID = %q", i, result.Record.Study.InstanceUID)
		}
	}
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := Read(context.Background(), source, Options{})
			if err == nil && result.Record.Study.InstanceUID != "1.2.3.replay" {
				err = errors.New("replayed record mismatch")
			}
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	short := ReadSeekerSource("", bytes.NewReader(encoded), 1)
	if _, err := Read(context.Background(), short, Options{}); err == nil {
		t.Fatal("size-bounded source error = nil")
	}
}

func TestReadAlwaysClosesOwnedSourceAndRedactsSourcePanics(t *testing.T) {
	closed := 0
	source := SourceFunc(func(context.Context) (OpenedSource, error) {
		return OpenedSource{
			Reader: bytes.NewReader([]byte("not dicom")),
			Info:   SourceInfo{Origin: "SECRET", Size: 9},
			Close: func() error {
				closed++
				return nil
			},
		}, nil
	})
	if _, err := Read(context.Background(), source, Options{}); err == nil {
		t.Fatal("Read() error = nil")
	}
	if closed != 1 {
		t.Fatalf("Close calls = %d, want 1", closed)
	}

	panicking := SourceFunc(func(context.Context) (OpenedSource, error) {
		panic("SECRET-SOURCE-PANIC")
	})
	_, err := Read(context.Background(), panicking, Options{})
	if err == nil || strings.Contains(err.Error(), "SECRET") || !errors.Is(err, ErrRead) {
		t.Fatalf("panic error = %v", err)
	}

}
