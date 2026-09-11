package netstore

import (
	"os"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/internal/dicomtags"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestPersistenceRejectsInvalidUIDWithoutSanitizingIdentity(t *testing.T) {
	for _, uid := range []string{"../escape", `..\escape`, "CON", "1.2/3", "1.2_3", "1..2", "1.02.3", "3.1", "1.40", "1.2\nremote", "1.2\x003", "1." + strings.Repeat("2", 63)} {
		t.Run(uid, func(t *testing.T) {
			dir := t.TempDir()
			ds := validDataSet()
			ds.Put(newUIElement(dicomtags.SOPInstanceUID, uid))
			if err := ValidateCStoreDataSet("1.2.3", uid, ul.AcceptedContext{AbstractSyntaxUID: "1.2.3"}, ds); err == nil {
				t.Error("accepted invalid command/dataset identity")
			}
			if path, err := SavePart10(dir, ds, transfer.ExplicitVRLittleEndian); err == nil || path != "" {
				t.Errorf("persisted invalid identity: path=%q error=%v", path, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Errorf("invalid identity created files: %v %v", entries, err)
			}
		})
	}
}
