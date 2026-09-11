package pixeldata

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/ThalesMMS/dicom-go/object"
)

type transcodePathTxn struct {
	sourcePath      string
	source          *os.File
	sourceInfo      os.FileInfo
	parentPath      string
	parent          *os.File
	parentInfo      os.FileInfo
	destinationName string
	previous        transcodeFileSnapshot
}

type stagedTranscodeFile struct {
	name       string
	file       *os.File
	open       bool
	entryOwned bool
}

func beginTranscodePathTxn(sourcePath, destinationPath string) (*transcodePathTxn, error) {
	sourceAbs, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, &TranscodeError{Stage: "source", Err: ErrTranscodeTransaction}
	}
	sourceParent, err := filepath.EvalSymlinks(filepath.Dir(sourceAbs))
	if err != nil {
		return nil, &TranscodeError{Stage: "source", Err: ErrTranscodeTransaction}
	}
	sourceAbs = filepath.Join(sourceParent, filepath.Base(sourceAbs))
	destinationAbs, err := filepath.Abs(destinationPath)
	if err != nil {
		return nil, &TranscodeError{Stage: "destination", Err: ErrTranscodeDestinationUnsafe}
	}
	parentPath, err := filepath.EvalSymlinks(filepath.Dir(destinationAbs))
	if err != nil {
		return nil, ErrTranscodeDestinationUnsafe
	}
	destinationAbs = filepath.Join(parentPath, filepath.Base(destinationAbs))
	if sourceAbs == destinationAbs {
		return nil, ErrTranscodeDestinationUnsafe
	}
	source, err := openTranscodeFileNoFollow(sourceAbs)
	if err != nil {
		return nil, &TranscodeError{Stage: "source", Err: ErrTranscodeTransaction}
	}
	sourceInfo, err := source.Stat()
	if err != nil || !sourceInfo.Mode().IsRegular() {
		_ = source.Close()
		return nil, &TranscodeError{Stage: "source", Err: ErrTranscodeTransaction}
	}
	parent, err := openTranscodeDirectoryNoFollow(parentPath)
	if err != nil {
		_ = source.Close()
		return nil, ErrTranscodeDestinationUnsafe
	}
	parentInfo, err := parent.Stat()
	if err != nil || !parentInfo.IsDir() {
		_ = parent.Close()
		_ = source.Close()
		return nil, ErrTranscodeDestinationUnsafe
	}
	destinationName := filepath.Base(destinationAbs)
	previous, err := transcodeDestinationSnapshotAt(parent, destinationName, sourceInfo)
	if err != nil {
		_ = parent.Close()
		_ = source.Close()
		return nil, err
	}
	if previous.exists && !transcodeCanReplaceExisting() {
		_ = parent.Close()
		_ = source.Close()
		return nil, ErrTranscodeDestinationUnsafe
	}
	return &transcodePathTxn{
		sourcePath: sourceAbs, source: source, sourceInfo: sourceInfo,
		parentPath: parentPath, parent: parent, parentInfo: parentInfo,
		destinationName: destinationName, previous: previous,
	}, nil
}

func (txn *transcodePathTxn) close() {
	if txn == nil {
		return
	}
	if txn.parent != nil {
		_ = txn.parent.Close()
	}
	if txn.source != nil {
		_ = txn.source.Close()
	}
}

func stageTranscodeFile(ctx context.Context, txn *transcodePathTxn, output *object.File, limits TranscodeLimits) (_ *stagedTranscodeFile, err error) {
	name, err := newTranscodeTempName()
	if err != nil {
		return nil, &TranscodeError{Stage: "temporary", Err: ErrTranscodeTransaction}
	}
	temp, err := createTranscodeFileAt(txn.parent, name)
	if err != nil {
		return nil, &TranscodeError{Stage: "temporary", Err: ErrTranscodeTransaction}
	}
	staged := &stagedTranscodeFile{name: name, file: temp, open: true, entryOwned: true}
	succeeded := false
	defer func() {
		if !succeeded {
			staged.cleanup(txn.parent)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return nil, &TranscodeError{Stage: "temporary", Err: ErrTranscodeTransaction}
	}
	writer := &transcodeBoundedWriter{ctx: ctx, writer: temp, remaining: limits.MaxOutputBytes}
	writeErr := object.WriteFile(writer, output)
	if writeErr == nil {
		writeErr = temp.Sync()
	}
	if writeErr == nil {
		_, writeErr = temp.Seek(0, 0)
	}
	if writeErr != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if errors.Is(writeErr, ErrTranscodeResourceLimit) {
			return nil, &TranscodeError{Stage: "write", Err: ErrTranscodeResourceLimit}
		}
		return nil, &TranscodeError{Stage: "write", Err: ErrTranscodeTransaction}
	}
	succeeded = true
	return staged, nil
}

func validateStagedTranscode(ctx context.Context, staged *stagedTranscodeFile, limits TranscodeLimits) error {
	readback, err := object.ReadFileWithOptions(&transcodeContextFile{File: staged.file, ctx: ctx}, object.ReadFileOptions{
		MaxElementBytes: limits.MaxOutputBytes, MaxTotalBytes: limits.MaxOutputBytes,
		MaxSequenceDepth: limits.MaxDepth, MaxElements: limits.MaxElements, MaxFragments: limits.MaxFragments,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return &TranscodeError{Stage: "readback", Err: ErrTranscodeTransaction}
	}
	_, validationErr := strictFileTransferSyntax(readback)
	if validationErr == nil && hasAnyPixelData(readback.Dataset) {
		validationErr = validateTranscodeReadback(readback.Dataset, readback.TransferSyntax, limits)
	} else if validationErr == nil && readback.TransferSyntax.Encapsulated {
		validationErr = ErrPixelDataNotFound
	}
	if validationErr != nil {
		return &TranscodeError{Stage: "readback", Err: ErrTranscodeTransaction}
	}
	return nil
}

func precommitTranscode(txn *transcodePathTxn, staged *stagedTranscodeFile) error {
	currentSource, err := openTranscodeFileNoFollow(txn.sourcePath)
	if err != nil {
		return ErrTranscodeSourceChanged
	}
	currentSourceInfo, statErr := currentSource.Stat()
	closeSourceErr := currentSource.Close()
	if statErr != nil || closeSourceErr != nil || !sameTranscodeFileInfo(txn.sourceInfo, currentSourceInfo) {
		return ErrTranscodeSourceChanged
	}
	currentParent, err := openTranscodeDirectoryNoFollow(txn.parentPath)
	if err != nil {
		return ErrTranscodeDestinationUnsafe
	}
	currentParentInfo, statErr := currentParent.Stat()
	closeParentErr := currentParent.Close()
	if statErr != nil || closeParentErr != nil || !os.SameFile(txn.parentInfo, currentParentInfo) {
		return ErrTranscodeDestinationUnsafe
	}
	if !sameTranscodeDestinationSnapshotAt(txn.parent, txn.destinationName, txn.previous) {
		return ErrTranscodeDestinationUnsafe
	}
	tempInfo, err := staged.file.Stat()
	if err != nil || !sameTranscodeEntryAt(txn.parent, staged.name, tempInfo) {
		return &TranscodeError{Stage: "temporary", Err: ErrTranscodeTransaction}
	}
	return nil
}

// publishStagedTranscode returns reportAvailable=true once the platform
// replacement completes without error. A subsequent durability error therefore
// carries the final report.
func publishStagedTranscode(ctx context.Context, txn *transcodePathTxn, staged *stagedTranscodeFile) (reportAvailable bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := replaceTranscodedFileAt(txn.parent, staged.file, staged.name, txn.destinationName, txn.previous); err != nil {
		if errors.Is(err, ErrTranscodeDestinationUnsafe) {
			return false, ErrTranscodeDestinationUnsafe
		}
		return false, &TranscodeError{Stage: "publish", Err: ErrTranscodeTransaction}
	}
	staged.open = false
	staged.entryOwned = false
	if err := syncTranscodeDirectory(txn.parent); err != nil {
		return true, &TranscodeError{Stage: "durability", Err: ErrTranscodeTransaction}
	}
	return true, nil
}

func (staged *stagedTranscodeFile) cleanup(parent *os.File) {
	if staged == nil {
		return
	}
	var ownedInfo os.FileInfo
	if staged.entryOwned {
		ownedInfo, _ = staged.file.Stat()
	}
	if staged.open {
		_ = staged.file.Close()
		staged.open = false
	}
	if staged.entryOwned && ownedInfo != nil && sameTranscodeEntryIdentityAt(parent, staged.name, ownedInfo) {
		_ = removeTranscodeFileAt(parent, staged.name)
	}
	staged.entryOwned = false
}

type transcodeFileSnapshot struct {
	exists bool
	info   os.FileInfo
}

func transcodeDestinationSnapshotAt(parent *os.File, name string, source os.FileInfo) (transcodeFileSnapshot, error) {
	file, err := openTranscodeFileAt(parent, name)
	if errors.Is(err, os.ErrNotExist) {
		return transcodeFileSnapshot{}, nil
	}
	if err != nil {
		return transcodeFileSnapshot{}, ErrTranscodeDestinationUnsafe
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !info.Mode().IsRegular() || os.SameFile(source, info) {
		return transcodeFileSnapshot{}, ErrTranscodeDestinationUnsafe
	}
	return transcodeFileSnapshot{exists: true, info: info}, nil
}

func sameTranscodeDestinationSnapshotAt(parent *os.File, name string, previous transcodeFileSnapshot) bool {
	file, err := openTranscodeFileAt(parent, name)
	if !previous.exists {
		return errors.Is(err, os.ErrNotExist)
	}
	if err != nil {
		return false
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	return statErr == nil && closeErr == nil && info.Mode().IsRegular() && sameTranscodeFileInfo(previous.info, info)
}

func sameTranscodeEntryAt(parent *os.File, name string, expected os.FileInfo) bool {
	file, err := openTranscodeFileAt(parent, name)
	if err != nil {
		return false
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	return statErr == nil && closeErr == nil && sameTranscodeFileInfo(expected, info)
}

func sameTranscodeEntryIdentityAt(parent *os.File, name string, expected os.FileInfo) bool {
	file, err := openTranscodeFileAt(parent, name)
	if err != nil {
		return false
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	return statErr == nil && closeErr == nil && expected != nil && os.SameFile(expected, info)
}

func sameTranscodeFileInfo(expected, current os.FileInfo) bool {
	return expected != nil && current != nil && os.SameFile(expected, current) && expected.Size() == current.Size() && expected.ModTime().Equal(current.ModTime())
}

func newTranscodeTempName() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".dicom-transcode-" + hex.EncodeToString(value[:]), nil
}

type transcodeContextFile struct {
	*os.File
	ctx context.Context
}

func (f *transcodeContextFile) Read(data []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(data)
}

func (f *transcodeContextFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Seek(offset, whence)
}

type transcodeBoundedWriter struct {
	ctx       context.Context
	writer    *os.File
	remaining int64
}

func (w *transcodeBoundedWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > w.remaining {
		return 0, ErrTranscodeResourceLimit
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}
