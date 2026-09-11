# JPEG XL Fixture Inventory

`jpegxl-inventory` builds a PHI-safe JSON manifest from a local directory of
DICOM JPEG XL fixtures. It records technical pixel metadata and stable hashes
without copying raw DICOM objects or raw Pixel Data fragments into the
repository.

The raw fixture files stay local unless publication is separately approved.
This document complements the broader fixture rules in
[`CODEC_FIXTURE_WORKFLOW.md`](CODEC_FIXTURE_WORKFLOW.md).

## Usage

Generate the default manifest from the sibling local fixture directory:

```sh
make jpegxl-inventory
```

Verify that the manifest still matches the local files:

```sh
make jpegxl-inventory-verify
```

Override paths when needed:

```sh
JPEGXL_FIXTURE_DIR=/path/to/JPEGXL-Fixture \
JPEGXL_MANIFEST=/tmp/jpegxl_manifest.json \
make jpegxl-inventory
```

The command can also write to stdout:

```sh
go run ./cmd/jpegxl-inventory -dir ../JPEGXL-Fixture
```

## Manifest

The committed manifest lives at:

```text
pixeldata/codecfixture/testdata/codecs/jpegxl_manifest.json
```

Top-level fields:

- `schemaVersion`: manifest schema version.
- `generatedAt`: UTC timestamp for the inventory run.
- `sourceDir`: the directory argument used for generation.
- `provenance`: included/excluded data policy and the local-only raw fixture
  policy.
- `summary`: total JPEG XL file count and counts by syntax group.
- `groups`: entries grouped by `JPEGXLLossless`,
  `JPEGXLJPEGRecompression`, and `JPEGXL`.

Each file entry includes only non-PHI technical fields:

- relative file path;
- file SHA-256;
- transfer syntax UID and name;
- rows, columns, samples per pixel, photometric interpretation;
- bits allocated, bits stored, pixel representation, frame count;
- encapsulation shape: fragment count, fragment sizes, offset table size,
  offset table hash when present, and per-fragment SHA-256 hashes.

## PHI Safety

Included:

- technical image and Pixel Data metadata needed to plan decoder work;
- stable file and fragment hashes;
- relative fixture paths.

Excluded:

- patient names;
- patient IDs;
- accession numbers;
- raw DICOM bytes;
- raw Pixel Data fragments.

The tool opens the local files to inspect required technical tags, but it never
writes patient-identifying fields or raw payload bytes into the manifest.

## Verification

Verification mode reads an existing manifest and rechecks every entry against
the current local directory. It fails non-zero when:

- a referenced file is missing;
- the file SHA-256 differs;
- required technical metadata cannot be read;
- Pixel Data is not encapsulated;
- fragment sizes or fragment SHA-256 values differ from the manifest.

The verification targets are opt-in and intentionally excluded from `make check`
because they depend on local-only files.
