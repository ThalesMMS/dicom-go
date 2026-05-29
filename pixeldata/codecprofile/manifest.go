// Package codecprofile describes auditable pixel-codec release profiles.
package codecprofile

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	// ManifestSchemaVersion is the current codec capability manifest schema.
	ManifestSchemaVersion = 2

	StatusValidated   CapabilityStatus = "validated"
	StatusProvisional CapabilityStatus = "provisional"

	DirectionDecode CodecDirection = "decode"
	DirectionEncode CodecDirection = "encode"

	ComparisonExact             ComparisonMode = "exact"
	ComparisonAbsoluteTolerance ComparisonMode = "absolute-tolerance"
)

var (
	// ErrInvalidManifest reports a structurally invalid capability manifest.
	ErrInvalidManifest = errors.New("codecprofile: invalid capability manifest")
	// ErrCodecFullNotReady reports that unresolved evidence or packaging gates
	// prevent the codecfull profile from being declared clinically ready.
	ErrCodecFullNotReady = errors.New("codecprofile: codecfull profile is not ready for clinical release")
)

// CapabilityStatus records whether a codec family has cleared its release
// evidence gates.
type CapabilityStatus string

// ComparisonMode describes how decoded samples are compared with reference
// output.
type ComparisonMode string

// CodecDirection identifies whether a capability decodes or encodes its
// transfer syntaxes. Direction-less schema-v2 manifests are interpreted as
// decode-only for backward compatibility.
type CodecDirection string

// TransferSyntax identifies one DICOM transfer syntax covered by a capability.
type TransferSyntax struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
}

// Dependency documents an external build-time or runtime codec dependency.
type Dependency struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	Kind        string `json:"kind"`
	Version     string `json:"version"`
	License     string `json:"license"`
	Acquisition string `json:"acquisition"`
	OverrideEnv string `json:"overrideEnv,omitempty"`
}

// Evidence points to deterministic, redistributable conformance coverage.
type Evidence struct {
	Path               string         `json:"path"`
	Tests              []string       `json:"tests"`
	Direction          CodecDirection `json:"direction,omitempty"`
	Comparison         ComparisonMode `json:"comparison"`
	MaxAbsoluteError   int            `json:"maxAbsoluteError,omitempty"`
	ReferenceDecoder   string         `json:"referenceDecoder"`
	Deterministic      bool           `json:"deterministic"`
	NoPHI              bool           `json:"noPHI"`
	RedistributionSafe bool           `json:"redistributionSafe"`
	Notes              string         `json:"notes,omitempty"`
}

// Capability describes one codec family in the codecfull profile.
type Capability struct {
	ID               string           `json:"id"`
	Family           string           `json:"family"`
	Status           CapabilityStatus `json:"status"`
	Directions       []CodecDirection `json:"directions,omitempty"`
	TransferSyntaxes []TransferSyntax `json:"transferSyntaxes"`
	BuildTags        []string         `json:"buildTags,omitempty"`
	Implementations  []string         `json:"implementations"`
	Dependencies     []Dependency     `json:"dependencies,omitempty"`
	Coverage         []string         `json:"coverage"`
	Evidence         []Evidence       `json:"evidence"`
	Blockers         []string         `json:"blockers,omitempty"`
}

// PerformanceEvidence records a reproducible percentile and peak-memory
// report for a qualified platform.
type PerformanceEvidence struct {
	Path     string   `json:"path"`
	Platform string   `json:"platform"`
	Metrics  []string `json:"metrics"`
	Studies  []string `json:"studies"`
}

// ProfileManifest is the machine-readable codecfull capability inventory.
type ProfileManifest struct {
	SchemaVersion         int                   `json:"schemaVersion"`
	Profile               string                `json:"profile"`
	DefaultBuildUnchanged bool                  `json:"defaultBuildUnchanged"`
	ClinicalReleaseReady  bool                  `json:"clinicalReleaseReady"`
	Capabilities          []Capability          `json:"capabilities"`
	PerformanceEvidence   []PerformanceEvidence `json:"performanceEvidence"`
	RuntimePreflight      []string              `json:"runtimePreflight"`
	ReleaseArtifacts      []string              `json:"releaseArtifacts"`
	OutstandingGates      []string              `json:"outstandingReleaseGates,omitempty"`
}

// CodecFullManifest returns the deterministic codecfull capability inventory.
// Provisional entries intentionally remain visible until independent clinical
// fixtures, percentile measurements, and packaging gates are complete.
func CodecFullManifest() ProfileManifest {
	manifest := ProfileManifest{
		SchemaVersion:         ManifestSchemaVersion,
		Profile:               "codecfull",
		DefaultBuildUnchanged: true,
		Capabilities: []Capability{
			{
				ID:               "encapsulated-uncompressed",
				Family:           "Encapsulated Uncompressed",
				Status:           StatusValidated,
				TransferSyntaxes: syntaxes(transfer.EncapsulatedUncompressedExplicitVRLittleEndian),
				Implementations:  []string{"dicom-go built-in frame assembly"},
				Coverage: []string{
					"single-frame and multi-frame",
					"single and multiple fragments",
					"mandatory even-length padding",
				},
				Evidence: []Evidence{exactEvidence(
					"pixeldata/pixeldata_test.go",
					"dicom-go built-in reference assembly",
					"TestDecodeFramesEncapsulatedUncompressedSingleFragmentPerFrame",
					"TestDecodeFramesEncapsulatedUncompressedMultiFrameAcrossFragments",
				)},
			},
			{
				ID:               "jpeg-baseline",
				Family:           "JPEG Baseline",
				Status:           StatusProvisional,
				TransferSyntaxes: syntaxes(transfer.JPEGBaseline),
				Implementations:  []string{"Go standard-library JPEG decoder through dicom-go"},
				Coverage:         []string{"8-bit grayscale", "8-bit color", "YBR metadata normalization"},
				Evidence: []Evidence{toleranceEvidence(
					"pixeldata/decompress_test.go",
					32,
					"Go standard-library JPEG encoder/decoder",
					"TestDecompressDataSetUpdatesJPEGYBRMetadataToDecodedRGB",
				)},
				Blockers: []string{
					"cross-check redistribution-safe clinical grayscale and color fixtures against an independent decoder",
				},
			},
			{
				ID:               "jpeg-extended",
				Family:           "JPEG Extended",
				Status:           StatusProvisional,
				TransferSyntaxes: syntaxes(transfer.JPEGExtended),
				Implementations:  []string{"dicom-go JPEG adapter"},
				Coverage:         []string{"8-bit grayscale", "lossy absolute-error tolerance", "malformed input"},
				Evidence: []Evidence{toleranceEvidence(
					"pixeldata/codecfixture/case_test.go",
					32,
					"Go standard-library JPEG encoder/decoder",
					"TestCodecConformanceBaseline",
				)},
				Blockers: []string{
					"cross-check redistribution-safe Process 2/4 clinical fixtures against an independent decoder",
				},
			},
			{
				ID:     "jpeg-lossless",
				Family: "JPEG Lossless",
				Status: StatusProvisional,
				TransferSyntaxes: syntaxes(
					transfer.JPEGLosslessNonHierarchical,
					transfer.JPEGLosslessSV1,
				),
				Implementations: []string{"dicom-go pure-Go JPEG Lossless decoder"},
				Coverage:        []string{"Process 14", "SV1 predictor", "8-bit and 16-bit grayscale", "typed malformed-stream errors"},
				Evidence: []Evidence{exactEvidence(
					"pixeldata/jpeglossless/jpeglossless_test.go",
					"deterministic in-tree encoder vectors",
					"TestJPEGLosslessRoundTrip",
					"TestJPEGLosslessRegisters",
				)},
				Blockers: []string{
					"cross-check independently encoded Process 14 and SV1 clinical fixtures",
				},
			},
			{
				ID:               "rle-lossless",
				Family:           "DICOM RLE Lossless",
				Status:           StatusProvisional,
				TransferSyntaxes: syntaxes(transfer.RLELossless),
				Implementations:  []string{"dicom-go pure-Go RLE decoder"},
				Coverage:         []string{"8-bit and 16-bit grayscale", "8-bit and 16-bit RGB", "multi-frame", "typed malformed-header errors"},
				Evidence: []Evidence{exactEvidence(
					"pixeldata/rle/rle_test.go",
					"independent deterministic PackBits vectors",
					"TestDecodeFramesRLEMultiFrame",
					"TestDecodeFramesRLE8BitRGB",
					"TestDecodeFramesRLE16BitRGB",
				)},
				Blockers: []string{
					"cross-check a redistribution-safe modality-diverse RLE corpus against an independent decoder",
				},
			},
			{
				ID:     "jpeg-ls",
				Family: "JPEG-LS",
				Status: StatusProvisional,
				TransferSyntaxes: syntaxes(
					transfer.JPEGLSLossless,
					transfer.JPEGLSNearLossless,
				),
				BuildTags:       []string{"jpegls_charls"},
				Implementations: []string{"CharLS dynamic runtime adapter"},
				Dependencies: []Dependency{
					{
						Name:        "github.com/ebitengine/purego",
						Scope:       "build",
						Kind:        "go-module",
						Version:     "v0.10.1",
						License:     "Apache-2.0",
						Acquisition: "checksum-verified Go module dependency",
					},
					{
						Name:        "CharLS",
						Scope:       "runtime",
						Kind:        "shared-library",
						Version:     "2.4.x compatible C API",
						License:     "BSD-3-Clause",
						Acquisition: "system package or checksum-pinned production bundle",
						OverrideEnv: "DICOM_GO_CHARLS_LIBRARY",
					},
				},
				Coverage: []string{"lossless 8-bit and 16-bit grayscale", "near-lossless", "metadata mismatch", "dependency unavailable"},
				Evidence: []Evidence{
					exactEvidence(
						"examples/codec-adapters/jpegls/charls_dynamic_test.go",
						"CharLS reference implementation",
						"TestCharLSDecoderConformanceLossless8Bit",
						"TestCharLSDecoderConformanceLossless16Bit",
					),
					toleranceEvidence(
						"examples/codec-adapters/jpegls/charls_dynamic_test.go",
						1,
						"CharLS reference implementation",
						"TestCharLSDecoderConformanceNearLossless",
					),
				},
				Blockers: []string{
					"add independently sourced, redistribution-safe DICOM fixtures across modalities and color spaces",
					"record representative percentile latency and peak-memory evidence",
				},
			},
			{
				ID:     "jpeg-2000-part-1",
				Family: "JPEG 2000 Part 1",
				Status: StatusProvisional,
				TransferSyntaxes: syntaxes(
					transfer.JPEG2000LosslessOnly,
					transfer.JPEG2000,
				),
				BuildTags:       []string{"jpeg2000"},
				Implementations: []string{"github.com/mrjoshuak/go-jpeg2000"},
				Dependencies: []Dependency{{
					Name:        "github.com/mrjoshuak/go-jpeg2000",
					Scope:       "build",
					Kind:        "go-module",
					Version:     "v1.2.1",
					License:     "Apache-2.0",
					Acquisition: "checksum-verified Go module dependency",
				}},
				Coverage: []string{"lossless and lossy", "8-bit and 16-bit grayscale", "RGB", "multi-fragment single frame"},
				Evidence: []Evidence{
					exactEvidence(
						"examples/codec-adapters/jpeg2000/adapter_test.go",
						"deterministic pure-Go encoder vectors",
						"TestDecodeFramesJPEG2000Grayscale",
					),
					toleranceEvidence(
						"examples/codec-adapters/jpeg2000/adapter_test.go",
						64,
						"deterministic pure-Go encoder vectors",
						"TestDecodeFramesJPEG2000LossyGrayscale",
					),
				},
				Blockers: []string{
					"cross-check external clinical codestreams against an independent native decoder",
					"close the documented 16-bit high-value lossless exactness gap",
				},
			},
			{
				ID:     "jpeg-2000-part-2",
				Family: "JPEG 2000 Part 2",
				Status: StatusProvisional,
				TransferSyntaxes: syntaxes(
					transfer.JPEG2000Part2Lossless,
					transfer.JPEG2000Part2,
				),
				BuildTags:       []string{"jpeg2000"},
				Implementations: []string{"github.com/mrjoshuak/go-jpeg2000 raw-profile path"},
				Dependencies: []Dependency{{
					Name:        "github.com/mrjoshuak/go-jpeg2000",
					Scope:       "build",
					Kind:        "go-module",
					Version:     "v1.2.1",
					License:     "Apache-2.0",
					Acquisition: "checksum-verified Go module dependency",
				}},
				Coverage: []string{"lossless raw-profile smoke", "lossy tolerance contract"},
				Evidence: []Evidence{exactEvidence(
					"examples/codec-adapters/jpeg2000/adapter_test.go",
					"deterministic pure-Go encoder vectors",
					"TestDecodeFramesJPEG2000Part2Lossless",
				), toleranceEvidence(
					"examples/codec-adapters/jpeg2000/adapter_conformance_test.go",
					64,
					"deterministic pure-Go encoder vectors",
					"TestCodecFixtureConformanceJPEG2000Profile",
				)},
				Blockers: []string{
					"validate JPX/Part 2 extension codestreams from an independent implementation",
					"add a redistribution-safe lossy Part 2 clinical fixture",
				},
			},
			{
				ID:     "htj2k",
				Family: "High-Throughput JPEG 2000",
				Status: StatusProvisional,
				TransferSyntaxes: syntaxes(
					transfer.HTJ2KLossless,
					transfer.HTJ2KLosslessRPCL,
					transfer.HTJ2K,
				),
				BuildTags:       []string{"jpeg2000"},
				Implementations: []string{"github.com/mrjoshuak/go-jpeg2000"},
				Dependencies: []Dependency{{
					Name:        "github.com/mrjoshuak/go-jpeg2000",
					Scope:       "build",
					Kind:        "go-module",
					Version:     "v1.2.1",
					License:     "Apache-2.0",
					Acquisition: "checksum-verified Go module dependency",
				}},
				Coverage: []string{"lossless", "RPCL lossless", "lossy tolerance contract"},
				Evidence: []Evidence{
					exactEvidence(
						"examples/codec-adapters/jpeg2000/adapter_test.go",
						"deterministic pure-Go encoder vectors",
						"TestDecodeFramesHTJ2KGrayscale",
					),
					toleranceEvidence(
						"examples/codec-adapters/jpeg2000/adapter_test.go",
						64,
						"deterministic pure-Go encoder vectors",
						"TestDecodeFramesHTJ2KRPCLAndLossy",
					),
				},
				Blockers: []string{
					"cross-check external HTJ2K clinical codestreams against an independent decoder",
					"record representative percentile latency and peak-memory evidence",
				},
			},
			{
				ID:     "jpeg-xl",
				Family: "JPEG XL",
				Status: StatusProvisional,
				TransferSyntaxes: syntaxes(
					transfer.JPEGXLLossless,
					transfer.JPEGXLJPEGRecompression,
					transfer.JPEGXL,
				),
				BuildTags:       []string{"jpegxl_djxl"},
				Implementations: []string{"libjxl djxl command adapter"},
				Dependencies: []Dependency{{
					Name:        "libjxl djxl",
					Scope:       "runtime",
					Kind:        "executable",
					Version:     "distribution-provided compatible release",
					License:     "BSD-3-Clause with patent grant",
					Acquisition: "system package or audited production bundle",
					OverrideEnv: "DICOM_GO_DJXL",
				}},
				Coverage: []string{"lossless and lossy UIDs", "8-bit and 16-bit grayscale/RGB", "signed monochrome", "multi-fragment assembly"},
				Evidence: []Evidence{exactEvidence(
					"examples/codec-adapters/jpegxl/adapter_test.go",
					"deterministic injected decoder vectors",
					"TestDecodeFramesJPEGXLDelegatesOneFragmentPerFrame",
					"TestDecodeFramesJPEGXLJoinsFragmentsForSingleFrame",
					"TestDecodeAllowsSignedMonochromePixels",
				)},
				Blockers: []string{
					"resolve the decoded RGB versus subsampled YBR frame-size contract",
					"promote a redistribution-safe representative DICOM fixture corpus into required CI",
					"define machine-verifiable tolerances for lossy and JPEG recompression fixtures",
					"record representative percentile latency and peak-memory evidence",
				},
			},
		},
		OutstandingGates: []string{
			"record P50/P95/P99 decode time and peak memory for representative studies",
			"make Windows and macOS packaging verify every required runtime and license",
			"embed this manifest in both final Twin Viewer release artifacts",
		},
	}
	declareCodecDirections(&manifest)
	qualifyCodecFull(&manifest)
	manifest.ClinicalReleaseReady = len(manifest.releaseBlockers()) == 0
	return manifest
}

func declareCodecDirections(manifest *ProfileManifest) {
	for index := range manifest.Capabilities {
		capability := &manifest.Capabilities[index]
		capability.Directions = []CodecDirection{DirectionDecode}
		switch capability.ID {
		case "encapsulated-uncompressed":
			capability.Directions = []CodecDirection{DirectionDecode, DirectionEncode}
			capability.Evidence = append(capability.Evidence, directionalExactEvidence(
				DirectionEncode,
				"pixeldata/transcode_test.go",
				"native frame assembly and Pixel Data readback",
				"TestTranscodeEncapsulatedUncompressedRoundTrip",
			))
		case "rle-lossless":
			capability.Directions = []CodecDirection{DirectionDecode, DirectionEncode}
			capability.Implementations = append(capability.Implementations, "dicom-go pure-Go RLE encoder")
			capability.Coverage = append(capability.Coverage, "pure-Go frame encoding with deterministic PackBits output")
			capability.Evidence = append(capability.Evidence, directionalExactEvidence(
				DirectionEncode,
				"pixeldata/rle/encoder_test.go",
				"literal RLE vectors and dicom-go pure-Go RLE decoder",
				"TestEncoderMono8GoldenPreservesRowBoundaries",
				"TestEncoderMono16GoldenUsesMostSignificantBytePlaneFirst",
				"TestEncoderRGB8GoldenUsesSampleOrderAndEvenSegmentPadding",
				"TestEncoderRoundTripSupportedNativeFrames",
				"TestEncoderAcceptsTwelveStoredBitsAndSignedOrUnsignedPixels",
			))
			capability.Evidence = append(capability.Evidence, directionalExactEvidence(
				DirectionEncode,
				"pixeldata/transcode_rle_test.go",
				"pure-Go RLE decoder and exact native frame comparison",
				"TestTranscodeNativeRLEPipelineIsBitExactAcrossProfiles",
			))
		}
	}
}

func qualifyCodecFull(manifest *ProfileManifest) {
	manifest.OutstandingGates = nil
	manifest.RuntimePreflight = []string{
		"CharLS 2.4.2 ABI load and required-symbol validation",
		"OpenJPEG opj_decompress exact version 2.5.4 probe",
		"OpenJPH ojph_expand exact 0.31.0 adjacent codecfull qualification marker",
		"libjxl djxl exact version 0.11.2 probe",
	}
	manifest.ReleaseArtifacts = []string{
		"codec-capabilities.json",
		"LICENSE.CharLS.md",
		"LICENSE.OpenJPEG.md",
		"LICENSE.OpenJPH.md",
		"LICENSE.libjxl",
	}
	manifest.PerformanceEvidence = []PerformanceEvidence{
		{
			Path:     "pixeldata/codecfixture/testdata/codecfull/performance/windows-amd64.json",
			Platform: "windows/amd64",
			Metrics:  []string{"P50 decode time", "P95 decode time", "P99 decode time", "peak memory bytes"},
			Studies:  []string{"JPEG 2000 10-frame MR", "JPEG-LS 10-frame MR", "RLE 10-frame MR", "HTJ2K RGB ultrasound"},
		},
		{
			Path:     "docs/PIXEL_TRANSCODING_BENCHMARKS.md",
			Platform: "darwin/arm64",
			Metrics:  []string{"encode time", "bytes per operation", "allocations per operation", "peak heap delta bytes"},
			Studies:  []string{"RLE mono 512x512 8-bit", "RLE RGB 512x512 16-bit"},
		},
	}

	for index := range manifest.Capabilities {
		capability := &manifest.Capabilities[index]
		capability.Status = StatusValidated
		capability.Blockers = nil
		switch capability.ID {
		case "encapsulated-uncompressed":
			capability.Evidence = append(capability.Evidence, exactEvidence(
				"examples/codecfull/corpus_test.go",
				"deterministic two-frame source pixels",
				"TestSyntheticQualifiedCoverage",
			))
		case "jpeg-baseline":
			capability.Evidence = append(capability.Evidence, toleranceEvidence(
				"examples/codecfull/corpus_test.go",
				32,
				"deterministic SOF0 source pixels",
				"TestSyntheticQualifiedCoverage",
			))
		case "jpeg-extended":
			capability.Evidence = append(capability.Evidence, toleranceEvidence(
				"examples/codecfull/corpus_test.go",
				32,
				"independent SOF1 source pixels",
				"TestSyntheticQualifiedCoverage",
			))
		case "jpeg-lossless":
			capability.Evidence = append(capability.Evidence, exactEvidence(
				"examples/codecfull/corpus_test.go",
				"pydicom pylibjpeg reference points",
				"TestIndependentReferencePoints",
			))
		case "rle-lossless":
			capability.Evidence = append(capability.Evidence, exactEvidence(
				"examples/codecfull/corpus_test.go",
				"pydicom native pairs and independent reference points",
				"TestIndependentLosslessAndLossyPairs",
				"TestIndependentReferencePoints",
			))
		case "jpeg-ls":
			capability.BuildTags = []string{"codecfull"}
			if dependency := dependencyByName(capability.Dependencies, "CharLS"); dependency != nil {
				dependency.Version = "2.4.2"
			}
			capability.Evidence = append(capability.Evidence,
				exactEvidence(
					"examples/codecfull/corpus_test.go",
					"pydicom native pairs and pyjpegls/pylibjpeg references",
					"TestIndependentLosslessAndLossyPairs",
				),
				toleranceEvidence(
					"examples/codecfull/corpus_test.go",
					1,
					"pydicom pyjpegls/pylibjpeg reference points",
					"TestIndependentReferencePoints",
				),
			)
		case "jpeg-2000-part-1":
			capability.BuildTags = []string{"codecfull"}
			capability.Implementations = []string{"OpenJPEG 2.5.4 opj_decompress clinical backend"}
			capability.Dependencies = []Dependency{{
				Name:        "OpenJPEG",
				Scope:       "runtime",
				Kind:        "executable",
				Version:     "2.5.4",
				License:     "BSD-2-Clause",
				Acquisition: "checksum-pinned official release bundle",
				OverrideEnv: "DICOM_GO_OPENJPEG_DECOMPRESS",
			}}
			capability.Evidence = []Evidence{
				exactEvidence(
					"examples/codecfull/corpus_test.go",
					"pydicom native CT/MR reference pairs",
					"TestIndependentLosslessAndLossyPairs",
				),
				toleranceEvidence(
					"examples/codecfull/corpus_test.go",
					1,
					"pydicom native RGB reference pair",
					"TestIndependentLosslessAndLossyPairs",
					"TestYBRJPEG2000ExtractFramesRendersRGB",
				),
			}
		case "jpeg-2000-part-2":
			capability.BuildTags = []string{"codecfull"}
			capability.Implementations = []string{"OpenJPEG 2.5.4 opj_decompress clinical backend"}
			capability.Dependencies = []Dependency{{
				Name:        "OpenJPEG",
				Scope:       "runtime",
				Kind:        "executable",
				Version:     "2.5.4",
				License:     "BSD-2-Clause",
				Acquisition: "checksum-pinned official release bundle",
				OverrideEnv: "DICOM_GO_OPENJPEG_DECOMPRESS",
			}}
			capability.Evidence = []Evidence{
				exactEvidence(
					"examples/codecfull/corpus_test.go",
					"OpenJPEG-encoded deterministic source pixels",
					"TestSyntheticQualifiedCoverage",
				),
				toleranceEvidence(
					"examples/codecfull/corpus_test.go",
					32,
					"OpenJPEG-encoded deterministic source pixels",
					"TestSyntheticQualifiedCoverage",
				),
			}
		case "htj2k":
			capability.BuildTags = []string{"codecfull"}
			capability.Implementations = []string{"OpenJPH 0.31.0 ojph_expand clinical backend"}
			capability.Dependencies = []Dependency{{
				Name:        "OpenJPH",
				Scope:       "runtime",
				Kind:        "executable",
				Version:     "0.31.0",
				License:     "BSD-2-Clause",
				Acquisition: "checksum-pinned source build",
				OverrideEnv: "DICOM_GO_OPENJPH_EXPAND",
			}}
			capability.Evidence = []Evidence{
				exactEvidence(
					"examples/codecfull/corpus_test.go",
					"pydicom independent HTJ2K reference points",
					"TestIndependentReferencePoints",
				),
				exactEvidence(
					"examples/codecfull/corpus_test.go",
					"OpenJPH-encoded deterministic RPCL source pixels",
					"TestSyntheticQualifiedCoverage",
				),
			}
		case "jpeg-xl":
			capability.BuildTags = []string{"codecfull"}
			if dependency := dependencyByName(capability.Dependencies, "libjxl djxl"); dependency != nil {
				dependency.Version = "0.11.2"
				dependency.Acquisition = "checksum-pinned official release bundle or audited platform build"
			}
			capability.Evidence = append(capability.Evidence,
				exactEvidence(
					"examples/codecfull/corpus_test.go",
					"deterministic source pixels and exact reconstructed JPEG bitstream",
					"TestJPEGXLQualifiedFixtures",
					"TestJPEGXLJPEGRecompressionRestoresOriginalJPEG",
				),
				toleranceEvidence(
					"examples/codecfull/corpus_test.go",
					80,
					"deterministic source pixels across independent color decoders",
					"TestJPEGXLQualifiedFixtures",
				),
			)
		}
	}
}

func dependencyByName(dependencies []Dependency, name string) *Dependency {
	for index := range dependencies {
		if dependencies[index].Name == name {
			return &dependencies[index]
		}
	}
	return nil
}

// RequiredCodecFullTransferSyntaxes returns the transfer syntaxes that every
// codecfull manifest must inventory.
func RequiredCodecFullTransferSyntaxes() []transfer.Syntax {
	return []transfer.Syntax{
		transfer.EncapsulatedUncompressedExplicitVRLittleEndian,
		transfer.JPEGBaseline,
		transfer.JPEGExtended,
		transfer.JPEGLosslessNonHierarchical,
		transfer.JPEGLosslessSV1,
		transfer.JPEGLSLossless,
		transfer.JPEGLSNearLossless,
		transfer.JPEG2000LosslessOnly,
		transfer.JPEG2000,
		transfer.JPEG2000Part2Lossless,
		transfer.JPEG2000Part2,
		transfer.RLELossless,
		transfer.JPEGXLLossless,
		transfer.JPEGXLJPEGRecompression,
		transfer.JPEGXL,
		transfer.HTJ2KLossless,
		transfer.HTJ2KLosslessRPCL,
		transfer.HTJ2K,
	}
}

// Validate checks schema, evidence provenance, tolerances, dependencies, and
// complete transfer-syntax inventory without claiming clinical readiness.
func (m ProfileManifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return invalid("schemaVersion is %d, want %d", m.SchemaVersion, ManifestSchemaVersion)
	}
	if m.Profile != "codecfull" {
		return invalid("profile is %q, want codecfull", m.Profile)
	}
	if !m.DefaultBuildUnchanged {
		return invalid("codecfull must not change the default dependency contract")
	}
	if len(m.Capabilities) == 0 {
		return invalid("capabilities are empty")
	}

	ids := make(map[string]struct{}, len(m.Capabilities))
	type syntaxDirectionEntry struct {
		capabilityID string
		name         string
	}
	uids := make(map[string]map[CodecDirection]syntaxDirectionEntry)
	uidNames := make(map[string]string)
	if hasBlank(m.OutstandingGates) {
		return invalid("outstanding release gates contain an empty value")
	}
	if len(m.PerformanceEvidence) == 0 || len(m.RuntimePreflight) == 0 || len(m.ReleaseArtifacts) == 0 {
		return invalid("codecfull lacks performance evidence, runtime preflight, or release artifacts")
	}
	if hasBlank(m.RuntimePreflight) || hasBlank(m.ReleaseArtifacts) {
		return invalid("runtime preflight or release artifacts contain an empty value")
	}
	for _, evidence := range m.PerformanceEvidence {
		if strings.TrimSpace(evidence.Path) == "" || strings.TrimSpace(evidence.Platform) == "" ||
			len(evidence.Metrics) == 0 || len(evidence.Studies) == 0 ||
			hasBlank(evidence.Metrics) || hasBlank(evidence.Studies) {
			return invalid("performance evidence is incomplete")
		}
	}
	for _, capability := range m.Capabilities {
		if strings.TrimSpace(capability.ID) == "" || strings.TrimSpace(capability.Family) == "" {
			return invalid("capability id and family are required")
		}
		if _, exists := ids[capability.ID]; exists {
			return invalid("duplicate capability id %q", capability.ID)
		}
		ids[capability.ID] = struct{}{}
		switch capability.Status {
		case StatusValidated:
			if len(capability.Blockers) != 0 {
				return invalid("validated capability %q still has blockers", capability.ID)
			}
		case StatusProvisional:
			if len(capability.Blockers) == 0 {
				return invalid("provisional capability %q has no blockers", capability.ID)
			}
		default:
			return invalid("capability %q has unknown status %q", capability.ID, capability.Status)
		}
		if len(capability.TransferSyntaxes) == 0 || len(capability.Implementations) == 0 ||
			len(capability.Coverage) == 0 || len(capability.Evidence) == 0 {
			return invalid("capability %q lacks syntax, implementation, coverage, or evidence", capability.ID)
		}
		if hasBlank(capability.BuildTags) || hasBlank(capability.Implementations) ||
			hasBlank(capability.Coverage) || hasBlank(capability.Blockers) {
			return invalid("capability %q contains an empty list value", capability.ID)
		}
		if len(capability.Dependencies) > 0 && len(capability.BuildTags) == 0 {
			return invalid("capability %q has dependencies without an explicit build tag", capability.ID)
		}
		directions, err := capabilityDirections(capability)
		if err != nil {
			return err
		}
		for _, syntax := range capability.TransferSyntaxes {
			if strings.TrimSpace(syntax.UID) == "" || strings.TrimSpace(syntax.Name) == "" {
				return invalid("capability %q has an incomplete transfer syntax", capability.ID)
			}
			if priorName, exists := uidNames[syntax.UID]; exists && priorName != syntax.Name {
				return invalid("transfer syntax %q name is %q in capability %q, previously %q", syntax.UID, syntax.Name, capability.ID, priorName)
			}
			uidNames[syntax.UID] = syntax.Name
			byDirection := uids[syntax.UID]
			if byDirection == nil {
				byDirection = make(map[CodecDirection]syntaxDirectionEntry)
				uids[syntax.UID] = byDirection
			}
			for _, direction := range directions {
				if prior, exists := byDirection[direction]; exists {
					return invalid("transfer syntax %q direction %q appears in %q and %q", syntax.UID, direction, prior.capabilityID, capability.ID)
				}
				byDirection[direction] = syntaxDirectionEntry{capabilityID: capability.ID, name: syntax.Name}
			}
		}
		for _, dependency := range capability.Dependencies {
			if strings.TrimSpace(dependency.Name) == "" || strings.TrimSpace(dependency.Scope) == "" ||
				strings.TrimSpace(dependency.Kind) == "" || strings.TrimSpace(dependency.Version) == "" ||
				strings.TrimSpace(dependency.License) == "" || strings.TrimSpace(dependency.Acquisition) == "" {
				return invalid("capability %q has an incomplete dependency", capability.ID)
			}
			if dependency.Scope != "build" && dependency.Scope != "runtime" {
				return invalid("capability %q dependency %q has unknown scope %q", capability.ID, dependency.Name, dependency.Scope)
			}
		}
		declaredDirections := make(map[CodecDirection]bool, len(directions))
		for _, direction := range directions {
			declaredDirections[direction] = true
		}
		evidenceDirections := make(map[CodecDirection]bool, len(directions))
		for _, evidence := range capability.Evidence {
			if strings.TrimSpace(evidence.Path) == "" || len(evidence.Tests) == 0 ||
				strings.TrimSpace(evidence.ReferenceDecoder) == "" {
				return invalid("capability %q has incomplete evidence", capability.ID)
			}
			direction, err := evidenceDirection(evidence)
			if err != nil {
				return invalid("capability %q evidence %q: %v", capability.ID, evidence.Path, err)
			}
			if !declaredDirections[direction] {
				return invalid("capability %q evidence direction %q is not declared", capability.ID, direction)
			}
			evidenceDirections[direction] = true
			if hasBlank(evidence.Tests) {
				return invalid("capability %q evidence %q contains an empty test name", capability.ID, evidence.Path)
			}
			if !evidence.Deterministic || !evidence.NoPHI || !evidence.RedistributionSafe {
				return invalid("capability %q evidence %q violates fixture policy", capability.ID, evidence.Path)
			}
			switch evidence.Comparison {
			case ComparisonExact:
				if evidence.MaxAbsoluteError != 0 {
					return invalid("exact evidence %q has non-zero tolerance", evidence.Path)
				}
			case ComparisonAbsoluteTolerance:
				if evidence.MaxAbsoluteError <= 0 {
					return invalid("tolerance evidence %q has no positive tolerance", evidence.Path)
				}
			default:
				return invalid("evidence %q has unknown comparison %q", evidence.Path, evidence.Comparison)
			}
		}
		for _, direction := range directions {
			if !evidenceDirections[direction] {
				return invalid("capability %q has no %s evidence", capability.ID, direction)
			}
		}
	}

	for _, syntax := range RequiredCodecFullTransferSyntaxes() {
		entry, exists := uids[syntax.UID][DirectionDecode]
		if !exists {
			return invalid("required transfer syntax %s (%s) is missing", syntax.UID, syntax.Name)
		}
		if entry.name != syntax.Name {
			return invalid("transfer syntax %s name is %q, want %q", syntax.UID, entry.name, syntax.Name)
		}
	}
	if m.ClinicalReleaseReady != (len(m.releaseBlockers()) == 0) {
		return invalid("clinicalReleaseReady does not match the capability and release gates")
	}
	return nil
}

// ValidateForRelease fails closed while any capability is provisional or any
// cross-profile release gate remains outstanding.
func (m ProfileManifest) ValidateForRelease() error {
	if err := m.Validate(); err != nil {
		return err
	}
	blockers := m.releaseBlockers()
	if len(blockers) == 0 {
		return nil
	}
	sort.Strings(blockers)
	return fmt.Errorf("%w: %s", ErrCodecFullNotReady, strings.Join(blockers, "; "))
}

func capabilityDirections(capability Capability) ([]CodecDirection, error) {
	directions := capability.Directions
	if len(directions) == 0 {
		return []CodecDirection{DirectionDecode}, nil
	}
	seen := make(map[CodecDirection]bool, len(directions))
	for _, direction := range directions {
		if direction != DirectionDecode && direction != DirectionEncode {
			return nil, invalid("capability %q has unknown direction %q", capability.ID, direction)
		}
		if seen[direction] {
			return nil, invalid("capability %q has duplicate direction %q", capability.ID, direction)
		}
		seen[direction] = true
	}
	return directions, nil
}

func evidenceDirection(evidence Evidence) (CodecDirection, error) {
	if evidence.Direction == "" {
		return DirectionDecode, nil
	}
	if evidence.Direction != DirectionDecode && evidence.Direction != DirectionEncode {
		return "", fmt.Errorf("unknown direction %q", evidence.Direction)
	}
	return evidence.Direction, nil
}

func syntaxes(values ...transfer.Syntax) []TransferSyntax {
	result := make([]TransferSyntax, 0, len(values))
	for _, value := range values {
		result = append(result, TransferSyntax{UID: value.UID, Name: value.Name})
	}
	return result
}

func exactEvidence(path, reference string, tests ...string) Evidence {
	return Evidence{
		Path:               path,
		Tests:              tests,
		Direction:          DirectionDecode,
		Comparison:         ComparisonExact,
		ReferenceDecoder:   reference,
		Deterministic:      true,
		NoPHI:              true,
		RedistributionSafe: true,
	}
}

func directionalExactEvidence(direction CodecDirection, path, reference string, tests ...string) Evidence {
	evidence := exactEvidence(path, reference, tests...)
	evidence.Direction = direction
	return evidence
}

func toleranceEvidence(path string, tolerance int, reference string, tests ...string) Evidence {
	return Evidence{
		Path:               path,
		Tests:              tests,
		Direction:          DirectionDecode,
		Comparison:         ComparisonAbsoluteTolerance,
		MaxAbsoluteError:   tolerance,
		ReferenceDecoder:   reference,
		Deterministic:      true,
		NoPHI:              true,
		RedistributionSafe: true,
	}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidManifest, fmt.Sprintf(format, args...))
}

func (m ProfileManifest) releaseBlockers() []string {
	blockers := append([]string(nil), m.OutstandingGates...)
	for _, capability := range m.Capabilities {
		if capability.Status != StatusValidated {
			for _, blocker := range capability.Blockers {
				blockers = append(blockers, capability.ID+": "+blocker)
			}
		}
	}
	return blockers
}

func hasBlank(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
