# Transfer Syntax recovery

`dicom-go` keeps Part 10 and raw dataset reading strict by default. The regular
`ReadFile`, `OpenFile`, `ReadDataSet`, and `OpenDataSet` APIs never infer a
Transfer Syntax. Recovery is available only through dedicated APIs that return
an auditable `TransferSyntaxResolution`.

```go
file, resolution, err := object.OpenFileWithTransferSyntaxRecovery(
	"legacy.dcm",
	object.ReadFileOptions{MaxTotalBytes: 512 << 20},
	object.TransferSyntaxRecoveryOptions{
		AllowMissingPreamble:          true,
		AllowMissingFileMeta:          true,
		AllowMissingTransferSyntaxUID: true,
		AllowUnknownTransferSyntaxUID: true,
		AllowDeclaredMismatch:         true,
		Probe: parser.TransferSyntaxProbeOptions{
			MinimumConfidence:    0.80,
			MinimumConfidenceGap: 0.15,
			MaxProbeBytes:        1 << 20,
			MaxCandidateBytes:    1 << 20,
			MaxElements:          256,
			MaxSequenceDepth:     16,
			MaxFragments:         64,
			MaxCandidates:        8,
			MinimumElements:      2,
			MaxTokens:            1024,
		},
	},
)
```

Each policy flag authorizes one non-conformant input condition. A declared
syntax that parses successfully always wins. A declared/dataset mismatch is
overridden only when `AllowDeclaredMismatch` is true and another candidate
passes both the absolute confidence threshold and the margin over the
runner-up. The original File Meta object is retained; recovery changes the
effective `File.TransferSyntax` but does not rewrite the source or silently
replace `(0002,0010)`.

## Probe contract

The probe reuses `parser.Reader` for every candidate. It does not implement a
second header parser. Evidence includes structurally valid tokens, bytes
consumed, tag ordering, valid explicit VR/reserved-byte layout, dictionary VR
agreement, plausible fixed-width value lengths, and valid sequence/item
nesting. Private and unknown tags are neutral evidence.

The default candidates are:

- Implicit VR Little Endian;
- Explicit VR Little Endian;
- Explicit VR Big Endian.

Compressed, deflated, encapsulated, unsupported, and media-payload candidates
are rejected by the probe. Their Pixel Data bytes are not reliable codec
evidence; callers must supply their Transfer Syntax explicitly through the
ordinary read APIs.

`TransferSyntaxProbeReport` distinguishes `selected`, `ambiguous`, and
`impossible`. Candidate diagnostics contain only syntax metadata, fixed reason
codes, conformance/failure classes, offsets, and counts. They never contain
element values, unknown File Meta UID text, or arbitrary parser error strings.
`ErrTransferSyntaxProbeAmbiguous` and
`ErrTransferSyntaxProbeImpossible` can be tested with `errors.Is`.

## Resource bounds and replay

`MaxProbeBytes` bounds the shared in-memory prefix, while
`MaxCandidateBytes`, `MaxElements`, `MaxSequenceDepth`, `MaxFragments`,
`MaxTokens`, `MaxCandidates`, and the candidate deadline bound each parser
pass. `MaxDuration` is a shared
cooperative deadline across all candidates. Time is checked between tokens;
an arbitrary blocking `io.Reader` can only be interrupted by the reader's own
context or deadline support.

Seekable sources are rewound for probing and the final parse, preserving
deferred-value offsets and source lifetime. Their probe prefix is capped by the
remaining logical `ReadFileOptions.MaxTotalBytes` budget as well as
`MaxProbeBytes`; the final parser enforces `MaxTotalBytes` again over the
logical dataset extent. Consequently a seekable prefix may be read twice, but
neither pass can exceed its configured bound. A non-seekable source is replayed
from one bounded buffer. `MaxNonSeekableBytes` defaults to 16 MiB and the call
fails with `ErrTransferSyntaxRecoveryBufferExceeded` if the complete source
does not fit. This avoids unbounded buffering and abandoned goroutines. For a
non-seekable source, `ReadFileOptions.MaxTotalBytes`, when set, is also a hard
upper bound on source bytes read while creating that replay buffer.

Recovery is intended for explicit local ingestion and inspection workflows. It
is not enabled automatically in C-STORE, STOW-RS, DIMSE datasets, or the Twin
Viewer.

## `dcmdump`

The CLI exposes the same opt-in policy:

```sh
go run ./cmd/dcmdump -recover-transfer-syntax legacy-or-raw.dcm
go run ./cmd/dcmdump -json -recover-transfer-syntax legacy-or-raw.dcm
```

It writes a warning with the inferred UID, source code, and confidence to
standard error. Text and JSON output also mark the syntax as inferred. Recovery
cannot currently be combined with `-show-offsets`, because the offset renderer
reparses a conformant Part 10 layout.
