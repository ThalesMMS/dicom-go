//go:build (jpegls_charls || codecfull) && (darwin || linux || windows)

package jpegls

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	purejls "github.com/ThalesMMS/dicom-go/pixeldata/jpegls"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCharLSDecodesPureGoNearLosslessEncoderFullSamples(t *testing.T) {
	if err := ValidateQualifiedRuntime(); err != nil {
		t.Fatal(err)
	}
	decoder := NewCharLSDecoder()
	// Optional candidate generation never edits the approved corpus. A fresh
	// explicit directory is required; normal test runs write no fixtures.
	outdir := os.Getenv("DICOM_GO_JPEGLS_ENCODER_CANDIDATES")
	if outdir != "" {
		if err := os.Mkdir(outdir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var records []map[string]any
	var samples uint64
	cases := codecfixture.JPEGLSNearEncoderCases()
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			encoder, err := purejls.NewEncoderWithOptions(purejls.EncoderOptions{Near: tc.Near, AllowLossy: true})
			if err != nil {
				t.Fatal(err)
			}
			registry := pixeldata.NewMemoryEncoderRegistry()
			if err := registry.RegisterEncoder(transfer.JPEGLSNearLossless.UID, encoder); err != nil {
				t.Fatal(err)
			}
			source := tc.Object()
			defer source.Close()
			derived, report, err := pixeldata.TranscodeDataSet(context.Background(), source, tc.Syntax, transfer.JPEGLSNearLossless, pixeldata.TranscodeOptions{EncoderRegistry: registry, AllowLossy: true})
			if err != nil {
				t.Fatal(err)
			}
			defer derived.Close()
			if !report.Lossy || report.Frames != len(tc.ExpectedFrames) {
				t.Fatal("transcode report contradicts frames")
			}
			pixel, err := pixeldata.Extract(derived)
			if err != nil {
				t.Fatal(err)
			}
			if len(pixel.Sequence.Fragments) != 2 {
				t.Fatal("incomplete encapsulated output")
			}
			m := tc.Metadata
			layout := codecfixture.SampleLayout{Rows: int(m.Rows), Columns: int(m.Columns), Components: int(m.SamplesPerPixel), BitsAllocated: int(m.BitsAllocated), BitsStored: int(m.BitsStored), HighBit: int(m.HighBit)}
			for i, original := range tc.ExpectedFrames {
				encoded, err := encoder.EncodeFrame(context.Background(), original, m)
				if err != nil {
					t.Fatal(err)
				}
				// The actual transcoder fragment must be the same codestream,
				// except its permitted even-length encapsulation padding.
				fragment := pixel.Sequence.Fragments[i]
				if len(fragment) < len(encoded.Data) || !bytes.Equal(fragment[:len(encoded.Data)], encoded.Data) || len(fragment)-len(encoded.Data) > 1 {
					t.Fatal("transcoder changed frame bytes")
				}
				got, err := decoder.DecodeJPEGLS(fragment, DecoderInput{Metadata: m, NearLossless: true})
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != len(original) {
					t.Fatal("incomplete independent reconstruction")
				}
				step := int(m.BitsAllocated / 8)
				for p := 0; p < len(got); p += step {
					a, b := int(got[p]), int(original[p])
					if step == 2 {
						a, b = int(binary.LittleEndian.Uint16(got[p:])), int(binary.LittleEndian.Uint16(original[p:]))
					}
					if a-b > tc.Near || b-a > tc.Near {
						t.Fatalf("CharLS source error exceeds NEAR at frame=%d sample=%d", i, p/step)
					}
					samples++
				}
				if outdir != "" {
					id := fmt.Sprintf("%s-f%d", tc.Name, i)
					paths := map[string]string{}
					hashes := map[string]string{}
					for suffix, data := range map[string][]byte{"jls": encoded.Data, "raw": got, "source.raw": original} {
						name := id + "." + suffix
						if err := os.WriteFile(filepath.Join(outdir, name), data, 0o600); err != nil {
							t.Fatal(err)
						}
						hash := sha256.Sum256(data)
						paths[suffix], hashes[suffix] = "jpegls-encode-near/"+name, hex.EncodeToString(hash[:])
					}
					const generator = "TestCharLSDecodesPureGoNearLosslessEncoderFullSamples v1"
					const oracle = "https://github.com/team-charls/charls/tree/36dd3307e070d8fbc765c3ba890b7e681046fa39"
					records = append(records, map[string]any{
						"id": id, "family": "jpeg-ls", "path": paths["jls"], "sha256": hashes["jls"], "referencePath": paths["raw"], "referenceSha256": hashes["raw"],
						"comparison": "exact-reconstruction", "modality": "OT", "bitsAllocated": m.BitsAllocated, "signed": false, "color": m.SamplesPerPixel == 3, "multiframe": false, "lossy": true,
						"generator": generator, "expectation": "encode-and-independent-decode-success", "baselineSkip": "", "qualificationLimit": fmt.Sprintf("unsigned %s ILV=0 NEAR=%d; frame %d of two distinct frames", m.PhotometricInterpretation, tc.Near, i),
						"input":          codecfixture.CorpusInput{TransferSyntax: transfer.JPEGLSNearLossless.UID, Rows: int(m.Rows), Columns: int(m.Columns), Components: int(m.SamplesPerPixel), BitsAllocated: int(m.BitsAllocated), BitsStored: int(m.BitsStored), HighBit: int(m.HighBit), Photometric: m.PhotometricInterpretation, Frames: 1},
						"reconstruction": codecfixture.ReconstructionEvidence{Layout: layout, Frames: 1, Backend: "CharLS C API", Version: QualifiedCharLSVersion, Source: oracle, Generator: generator},
						"sourceSamples":  codecfixture.SourceSampleEvidence{Path: paths["source.raw"], SHA256: hashes["source.raw"], Near: tc.Near},
						"provenance":     codecfixture.Provenance{Source: "codecfixture.JPEGLSNearEncoderCases v1; " + oracle, Synthetic: true, NoPHI: true, License: "MIT", Permission: "Project-authored synthetic samples; redistributable", Notes: "Pure-Go encoder output independently decoded by pinned CharLS; no external code embedded"},
					})
				}
			}
		})
	}
	if t.Failed() {
		return
	}
	if outdir != "" {
		data, err := json.MarshalIndent(map[string]any{"charlsVersion": QualifiedCharLSVersion, "charlsCommit": "36dd3307e070d8fbc765c3ba890b7e681046fa39", "fixtures": records}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outdir, "generation.json"), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("independent CharLS: %d profiles, %d frames, %d samples, complete NEAR bound", len(cases), len(cases)*2, samples)
}
