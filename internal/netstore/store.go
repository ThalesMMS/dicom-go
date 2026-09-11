package netstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtags"
	"github.com/ThalesMMS/dicom-go/internal/nofollow"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var ErrInvalidIdentity = errors.New("netstore: invalid SOP identity")
var ErrUnsafeDirectory = errors.New("netstore: output directory changed or unsafe")
var ErrCollisionLimit = errors.New("netstore: unique instance limit reached")

// Error exposes an operation and publication state, never a UID, path or remote
// value. Unwrap preserves the cause for errors.Is/As; do not log the cause raw.
type Error struct {
	Operation string
	Published bool
	cause     error
}

func (e *Error) Error() string {
	if e.Published {
		return "netstore: " + e.Operation + " failed after publication"
	}
	return "netstore: " + e.Operation + " failed"
}
func (e *Error) Unwrap() error                    { return e.cause }
func failure(operation string, cause error) error { return &Error{Operation: operation, cause: cause} }

// Dataset UIDs accept only the existing UI padding normalization. Multiplicity,
// VR and canonical identity are validated separately from filename construction.
func dataSetIdentity(dataset *object.Object) (string, string, error) {
	if dataset == nil {
		return "", "", errors.New("missing dataset")
	}
	class, ok := dataset.GetUIDs(dicomtags.SOPClassUID)
	if !ok || len(class) == 0 {
		return "", "", object.ErrMissingSOPClassUID
	}
	instance, ok := dataset.GetUIDs(dicomtags.SOPInstanceUID)
	if !ok || len(instance) == 0 {
		return "", "", object.ErrMissingSOPInstanceUID
	}
	if len(class) != 1 || len(instance) != 1 || !core.IsValidUID(class[0]) || !core.IsValidUID(instance[0]) {
		return "", "", ErrInvalidIdentity
	}
	return class[0], instance[0], nil
}

func ValidateCStoreDataSet(affectedSOPClassUID, affectedSOPInstanceUID string, pc ul.AcceptedContext, dataset *object.Object) error {
	class, instance, err := dataSetIdentity(dataset)
	if err != nil {
		return err
	}
	if !core.IsValidUID(affectedSOPClassUID) || !core.IsValidUID(affectedSOPInstanceUID) || !core.IsValidUID(pc.AbstractSyntaxUID) {
		return ErrInvalidIdentity
	}
	if pc.AbstractSyntaxUID != affectedSOPClassUID || class != affectedSOPClassUID || instance != affectedSOPInstanceUID {
		return ErrInvalidIdentity
	}
	return nil
}

// SavePart10 publishes only a complete file. Use SavePart10WithContext for a
// cancelable transfer. Neither function silently repairs invalid SOP identities.
func SavePart10(outDir string, dataset *object.Object, syntax transfer.Syntax) (string, error) {
	return SavePart10WithContext(context.Background(), outDir, dataset, syntax)
}
func SavePart10WithContext(ctx context.Context, outDir string, dataset *object.Object, syntax transfer.Syntax) (string, error) {
	return savePart10(ctx, outDir, dataset, syntax, defaultSaveOperations())
}

func part10File(dataset *object.Object, syntax transfer.Syntax, class, instance string) *object.File {
	return &object.File{Meta: object.FromElements([]core.Element{
		newUIElement(dicomtags.MediaStorageSOPClassUID, class),
		newUIElement(dicomtags.MediaStorageSOPInstanceUID, instance),
		newUIElement(dicomtags.TransferSyntaxUID, syntax.UID),
	}, std.Dictionary), Dataset: dataset, TransferSyntax: syntax}
}

// CreateInstanceFile is a low-level exclusive reservation, not publication of a
// complete instance. CLI persistence uses SavePart10WithContext instead.
func CreateInstanceFile(outDir, sopInstanceUID string) (string, *os.File, error) {
	return createInstanceFile(outDir, sopInstanceUID, protectInstanceFile)
}
func createInstanceFile(outDir, sopInstanceUID string, protect func(*os.File) error) (string, *os.File, error) {
	if !core.IsValidUID(sopInstanceUID) {
		return "", nil, ErrInvalidIdentity
	}
	parent, err := nofollow.OpenDirectory(outDir)
	if err != nil {
		return "", nil, failure("open output", err)
	}
	defer parent.Close()
	for i := 0; i < 1000; i++ {
		name := instanceName(sopInstanceUID, i)
		f, err := nofollow.CreateAt(parent, name)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, failure("create instance", err)
		}
		if err := protect(f); err != nil {
			cause := errors.Join(err, f.Close(), nofollow.RemoveAt(parent, name))
			return "", nil, failure("protect instance", cause)
		}
		return filepath.Join(outDir, name), f, nil
	}
	return "", nil, ErrCollisionLimit
}
func instanceName(uid string, collision int) string {
	if collision == 0 {
		return uid + ".dcm"
	}
	return fmt.Sprintf("%s.%d.dcm", uid, collision)
}

// SafeFileBase is a legacy display/name transform, not UID validation. It is
// never used to authorize identity or choose a received-instance filename.
func SafeFileBase(uid string) string {
	uid = core.NormalizeUID(uid)
	if uid == "" {
		return "instance"
	}
	var b strings.Builder
	for _, r := range uid {
		if (r >= '0' && r <= '9') || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	if b.Len() == 0 {
		return "instance"
	}
	name := b.String()
	if name[0] == '.' {
		name = "_" + name[1:]
	}
	return name
}

func newUIElement(tag core.Tag, value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRUI},
		Value:  core.StringValue{value},
	}
}
