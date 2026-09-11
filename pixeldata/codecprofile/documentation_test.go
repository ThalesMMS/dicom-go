package codecprofile

import (
	"os"
	"strings"
	"testing"
)

func TestCanonicalCapabilityMatrixMatchesManifest(t *testing.T) {
	data, err := os.ReadFile("../../docs/CODEC_CAPABILITY_MATRIX.md")
	if err != nil {
		t.Fatal(err)
	}
	document := string(data)
	directions := make(map[string]map[CodecDirection]bool)
	lines := strings.Split(document, "\n")
	for _, capability := range CodecFullManifest().Capabilities {
		for _, syntax := range capability.TransferSyntaxes {
			if directions[syntax.UID] == nil {
				directions[syntax.UID] = make(map[CodecDirection]bool)
			}
			for _, direction := range capability.Directions {
				directions[syntax.UID][direction] = true
			}
			line := matrixLineForUID(lines, syntax.UID)
			if line == "" {
				t.Errorf("matrix is missing %s (%s)", syntax.UID, syntax.Name)
				continue
			}
			for _, tag := range capability.BuildTags {
				if !strings.Contains(line, "`"+tag+"`") {
					t.Errorf("matrix row %s is missing build tag %s", syntax.UID, tag)
				}
			}
			for _, dependency := range capability.Dependencies {
				for _, token := range []string{dependency.Name, dependency.Version, dependency.License} {
					if token != "" && !strings.Contains(line, token) {
						t.Errorf("matrix row %s is missing dependency token %q", syntax.UID, token)
					}
				}
			}
		}
	}
	for uid, supported := range directions {
		line := matrixLineForUID(lines, uid)
		columns := strings.Split(line, "|")
		if len(columns) < 5 {
			t.Errorf("matrix row %s is malformed", uid)
			continue
		}
		decode := strings.Contains(strings.TrimSpace(columns[2]), "yes")
		encode := strings.Contains(strings.TrimSpace(columns[3]), "yes")
		if decode != supported[DirectionDecode] || encode != supported[DirectionEncode] {
			t.Errorf("matrix row %s directions decode=%t encode=%t, manifest decode=%t encode=%t", uid, decode, encode, supported[DirectionDecode], supported[DirectionEncode])
		}
	}
}

func matrixLineForUID(lines []string, uid string) string {
	for _, line := range lines {
		if strings.Contains(line, "(`"+uid+"`)") {
			return line
		}
	}
	return ""
}
