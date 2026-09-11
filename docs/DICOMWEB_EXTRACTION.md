# DICOMweb ownership boundary

The Twin adapter delegates reusable request/response mechanics to
`net/dicomweb`:

- QIDO-RS study search, WADO-RS metadata/retrieve, and STOW-RS store client
  calls live in `dicom-go/net/dicomweb`.
- QIDO-RS study criteria, return fields, and DICOM JSON study-match extraction
  live in `net/dicomweb`.
- Dynamic bearer-token injection, in-memory access-token lifecycle, serialized
  refresh, redaction, and one retry of a challenged replayable request live in
  `net/dicomweb`. Streaming STOW-RS bodies are never replayed.
- DICOM JSON value extraction lives in `dicom-go/dicomjson`.

Twin entry points that remain thin or application-owned are:

- `Twin-Viewer/internal/backend/dicomweb`: node-to-endpoint mapping,
  OAuth2/OIDC discovery and login flows, OS-protected refresh-credential
  storage, TLS/client options, typed Twin error adaptation, and mapping neutral
  DICOMweb study matches to `query.Match`.
- `Twin-Viewer/internal/backend/netverify`, `query`, `retrieve`, `send` and
  `receive`: DIMSE verify/query/retrieve/send/receive adapters that should stay
  thin over neutral `net/dimse` and `net/ul` APIs.
- `Twin-Viewer/internal/backend/session/operations.go`: session operation
  dispatch wiring that must remain Twin-owned.

Reusable QIDO-RS, WADO-RS and STOW-RS request/response helpers and bearer-token
application belong in `dicom-go/net/dicomweb`. Application endpoint
profiles, OAuth2/OIDC protocol flows, persistent credential storage,
authorization policy, jobs, archive import/export, receiver policy, operation
history, UI summaries and auto-query remain Twin/application responsibilities.
