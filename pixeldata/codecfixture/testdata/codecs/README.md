# Codec Fixture Testdata

This directory is reserved for on-disk codec fixtures used by
`pixeldata/codecfixture`.

Synthetic fixtures remain the default and should usually be generated in Go
tests instead of checked in as binary files. Any de-identified fixture committed
here must include adjacent provenance notes with:

- source and generation history;
- license or permission details;
- explicit no-PHI statement;
- de-identification method.

Fixtures without that metadata should not be committed.
