# Non-patient discovery and retrieval

Run `go run ./examples/nonpatientqr` from the library module. The example opens
only loopback listeners, discovers one synthetic object for each model through
C-FIND, then uses its returned UID in C-GET and verifies the reverse C-STORE
dataset and command identity. Both operations share the existing AsyncSession.
The finite catalog is supplied by the application; nothing is persisted.

These deliberately minimal objects exercise Q/R identifiers and transport;
they are not complete Hanging Protocol or Color Palette IOD examples and are
not intended for import into a clinical viewer. Full IOD construction and
validation remain the catalog application's responsibility. No patient data,
external node, filesystem paths, deployment credentials or workflows are used.
See [the profile contract](../../docs/NON_PATIENT_QR.md).
