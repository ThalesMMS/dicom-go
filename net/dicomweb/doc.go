// Package dicomweb provides neutral DICOMweb client and embeddable server
// primitives for QIDO-RS, WADO-RS, and STOW-RS endpoints.
//
// The package owns reusable request construction, response parsing, typed
// HTTP/DICOMweb errors, and bearer-token injection. TokenManager provides an
// in-memory access-token cache with serialized refresh and one safe retry after
// a challenged replayable request. Applications remain responsible for endpoint
// profiles, OAuth2/OIDC protocol flows, OS-protected credential persistence,
// authorization policy, jobs, archive import/export, and user-facing state.
//
// Server is deny-by-default: callers must provide an Authorizer or explicitly
// set AllowUnauthenticated. It supplies bounded routing, DICOM JSON response
// validation, multipart streaming, complete-request STOW staging, graceful
// shutdown, and PHI-free audit events. Storage identity and duplicate policy
// are atomic backend responsibilities. See docs/DICOMWEB_SERVER.md for the
// implemented PS3.18 profile and intentional limitations.
package dicomweb
