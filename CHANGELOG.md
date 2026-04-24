# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses Go module semantic version tags.

## [0.1.0] - 2026-04-24

Initial public release of `dicom-go`.

### Added

- M0: repository architecture, package boundaries, Makefile quality targets and
  contribution baseline.
- M1: dataset parser support for native transfer syntaxes, sequences, items,
  fragments and defensive read limits.
- M2: writer support for raw elements, datasets and Part 10 files.
- M3: generated standard tag dictionary and standard UID registry.
- M4: native uncompressed pixel data metadata and frame extraction.
- M5: DICOM Upper Layer PDU codec and association negotiation.
- M6: DICOM JSON marshal/unmarshal support.
- M7: minimal DIMSE C-ECHO and C-STORE support with SCU/SCP helpers.
- M8: user documentation, runnable examples, conformance scope and release
  notes.
- Part 10 file read/write through the `object` package.
- Native transfer syntax support for Implicit VR Little Endian, Explicit VR
  Little Endian and Explicit VR Big Endian.
- Encapsulated Pixel Data fragment parsing/preservation.
- Optional DICOM RLE Lossless codec in `pixeldata/rle`.
- CLI tools: `dcmdump`, `echoscu`, `echoscp`, `storescu`, `storescp`,
  `dicomuidgen`, and dictionary generation tools.
- Runnable examples under `examples/`.

### Known Limitations

- No claim of full DICOM conformance.
- No TLS or user identity negotiation.
- Does not include C-FIND, C-MOVE, C-GET, N-* services or Storage Commitment.
- No automatic transcoding.
- Not supported: JPEG, JPEG-LS, JPEG 2000, JPEG XL, MPEG or HEVC pixel decoding.
- Parser limits are opt-in and should be configured for untrusted input.
