package netstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ThalesMMS/dicom-go/internal/nofollow"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Per-call operations permit deterministic I/O fault testing without global hooks.
type saveOperations struct {
	write   func(io.Writer, *object.File) error
	sync    func(*os.File) error
	close   func(*os.File) error
	publish func(*os.File, string, string, os.FileInfo) (bool, error)
}

func defaultSaveOperations() saveOperations {
	return saveOperations{object.WriteFile, (*os.File).Sync, (*os.File).Close, nofollow.PublishClosedAt}
}

func savePart10(ctx context.Context, outDir string, dataset *object.Object, syntax transfer.Syntax, ops saveOperations) (path string, result error) {
	class, instance, err := dataSetIdentity(dataset)
	if err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", failure("prepare", err)
	}
	parent, err := nofollow.OpenDirectory(outDir)
	if err != nil {
		return "", failure("open output", err)
	}
	defer parent.Close()
	parentInfo, err := parent.Stat()
	if err != nil {
		return "", failure("inspect output", err)
	}
	var f *os.File
	var temporary string
	for attempt := 0; attempt < 10; attempt++ {
		var token [16]byte
		if _, err = rand.Read(token[:]); err != nil {
			return "", failure("prepare", err)
		}
		temporary = ".netstore-" + hex.EncodeToString(token[:]) + ".partial"
		f, err = nofollow.CreateAt(parent, temporary)
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		return "", failure("create temporary", err)
	}
	defer f.Close()
	published := false
	defer func() {
		if err := nofollow.RemoveAt(parent, temporary); err != nil {
			result = &Error{Operation: "cleanup", Published: published, cause: errors.Join(result, err)}
		}
	}()
	if err := protectInstanceFile(f); err != nil {
		return "", failure("protect temporary", err)
	}
	file := part10File(dataset, syntax, class, instance)
	if err := ops.write(contextWriter{ctx: ctx, destination: f}, file); err != nil {
		return "", failure("write instance", err)
	}
	if err := ctx.Err(); err != nil {
		return "", failure("write instance", err)
	}
	if err := ops.sync(f); err != nil {
		return "", failure("sync instance", err)
	}
	info, err := f.Stat()
	if err != nil {
		return "", failure("inspect temporary", err)
	}
	if err := ops.close(f); err != nil {
		return "", failure("close instance", err)
	}
	// Reopen using the same no-follow traversal, not a textual prefix comparison.
	// Creation, publication and cleanup stay anchored to the original descriptor.
	current, err := nofollow.OpenDirectory(outDir)
	if err != nil {
		return "", failure("verify output", errors.Join(ErrUnsafeDirectory, err))
	}
	currentInfo, statErr := current.Stat()
	closeErr := current.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(parentInfo, currentInfo) {
		return "", failure("verify output", errors.Join(ErrUnsafeDirectory, statErr, closeErr))
	}
	for i := 0; i < 1000; i++ {
		if err := ctx.Err(); err != nil {
			return "", failure("publish instance", err)
		}
		name := instanceName(instance, i)
		published, err = ops.publish(parent, temporary, name, info)
		if published {
			path = filepath.Join(outDir, name)
			if err != nil {
				return path, &Error{Operation: "finalize instance", Published: true, cause: err}
			}
			// Content was synced before publication. On Unix also sync the directory
			// entry. Windows does not provide the same directory-fsync contract.
			if runtime.GOOS != "windows" {
				if err := parent.Sync(); err != nil {
					return path, &Error{Operation: "sync publication", Published: true, cause: err}
				}
			}
			return path, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", failure("publish instance", err)
		}
	}
	return "", ErrCollisionLimit
}

type contextWriter struct {
	ctx         context.Context
	destination io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if err := w.ctx.Err(); err != nil {
			return written, err
		}
		chunk := min(len(p), 32<<10)
		n, err := w.destination.Write(p[:chunk])
		written += n
		if err != nil {
			return written, err
		}
		if n != chunk {
			return written, io.ErrShortWrite
		}
		p = p[n:]
	}
	return written, nil
}
