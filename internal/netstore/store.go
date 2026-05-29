package netstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtags"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func ValidateCStoreDataSet(affectedSOPClassUID, affectedSOPInstanceUID string, pc ul.AcceptedContext, dataset *object.Object) error {
	if dataset == nil {
		return errors.New("missing dataset")
	}
	if affectedSOPClassUID == "" {
		return errors.New("missing affected SOP Class UID")
	}
	if affectedSOPInstanceUID == "" {
		return errors.New("missing affected SOP Instance UID")
	}
	if pc.AbstractSyntaxUID != affectedSOPClassUID {
		return fmt.Errorf("presentation context SOP Class UID %q does not match request %q", pc.AbstractSyntaxUID, affectedSOPClassUID)
	}
	if sopClassUID, ok := dataset.GetUID(dicomtags.SOPClassUID); !ok || sopClassUID != affectedSOPClassUID {
		return errors.New("dataset SOP Class UID does not match request")
	}
	if sopInstanceUID, ok := dataset.GetUID(dicomtags.SOPInstanceUID); !ok || sopInstanceUID != affectedSOPInstanceUID {
		return errors.New("dataset SOP Instance UID does not match request")
	}
	return nil
}

func SavePart10(outDir string, dataset *object.Object, syntax transfer.Syntax) (string, error) {
	if dataset == nil {
		return "", errors.New("missing dataset")
	}
	sopClassUID, ok := dataset.GetUID(dicomtags.SOPClassUID)
	if !ok || sopClassUID == "" {
		return "", object.ErrMissingSOPClassUID
	}
	sopInstanceUID, ok := dataset.GetUID(dicomtags.SOPInstanceUID)
	if !ok || sopInstanceUID == "" {
		return "", object.ErrMissingSOPInstanceUID
	}

	path, f, err := CreateInstanceFile(outDir, sopInstanceUID)
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		_ = f.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()

	file := &object.File{
		Meta: object.FromElements([]core.Element{
			newUIElement(dicomtags.MediaStorageSOPClassUID, sopClassUID),
			newUIElement(dicomtags.MediaStorageSOPInstanceUID, sopInstanceUID),
			newUIElement(dicomtags.TransferSyntaxUID, syntax.UID),
		}, std.Dictionary),
		Dataset:        dataset,
		TransferSyntax: syntax,
	}
	if err := object.WriteFile(f, file); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	committed = true
	return path, nil
}

func CreateInstanceFile(outDir, sopInstanceUID string) (string, *os.File, error) {
	return createInstanceFile(outDir, sopInstanceUID, protectInstanceFile)
}

func createInstanceFile(outDir, sopInstanceUID string, protect func(string) error) (string, *os.File, error) {
	base := SafeFileBase(sopInstanceUID)
	for i := 0; i < 1000; i++ {
		name := base + ".dcm"
		if i > 0 {
			name = fmt.Sprintf("%s.%d.dcm", base, i)
		}
		path := filepath.Join(outDir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if err := protect(path); err != nil {
				closeErr := f.Close()
				removeErr := os.Remove(path)
				return "", nil, errors.Join(
					fmt.Errorf("protect DICOM instance file: %w", err),
					closeErr,
					removeErr,
				)
			}
			return path, f, nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return "", nil, err
	}
	return "", nil, fmt.Errorf("could not create unique file for SOP Instance UID hash %s", safeID(sopInstanceUID))
}

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

func safeID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func newUIElement(tag core.Tag, value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRUI},
		Value:  core.StringValue{value},
	}
}
