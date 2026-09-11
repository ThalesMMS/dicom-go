# Repository Guidelines

## Project Structure & Module Organization

This is a Go 1.22 module for DICOM file, JSON, pixel data, dictionary, and networking support. Networking includes reusable DIMSE and DICOMweb protocol/client primitives; application catalogs, node stores, credential policy, jobs, and UI orchestration stay outside this module. Public packages live at the repository root and in `core/`, `parser/`, `object/`, `transfer/`, `dictionary/`, `dicomjson/`, `pixeldata/`, and `net/`. Command-line tools are under `cmd/` (`dcmdump`, `echoscu`, `storescp`, `findscu`, and related tools). Internal helpers and generated standard data live in `internal/`. Examples are in `examples/`, with a separate viewer module in `examples/viewer/`. Design notes, conformance scope, roadmap material, and interop setup live in `docs/`; update them when behavior changes.

## Build, Test, and Development Commands

- `make fmt`: run `gofmt -w .` across the repo.
- `make fmt-check`: report files that are not gofmt-formatted.
- `make vet`: run `go vet ./...`.
- `make test`: run `go test ./...`.
- `make build`: compile all packages and commands with `go build ./...`.
- `make check`: run the full local baseline: formatting, vet, tests, and build.

For CLI smoke checks, use `go run ./cmd/dcmdump -- image.dcm` or `go run ./examples/readfile -- image.dcm`.

## Coding Style & Naming Conventions

Use standard Go formatting and idioms. Package names should be lower-case without underscores. Exported identifiers use `PascalCase`; unexported identifiers use `camelCase`. Keep package boundaries narrow: parsing logic belongs in `parser/`, dataset/file behavior in `object/`, transfer syntax concerns in `transfer/`, DIMSE/UL networking in `net/ul` and `net/dimse`, and reusable DICOMweb helpers in `net/dicomweb` or another `net/...` package when added. Prefer explicit error returns and small table-driven helpers over hidden global state.

## Testing Guidelines

Place tests next to code as `*_test.go`, and name test functions `TestXxx`. Use table-driven tests for parser, encoder, dictionary, transfer syntax, and command behavior. Golden outputs are kept under package-local `testdata/` directories, for example `internal/dcmdump/testdata/`. External peer tests are opt-in; set `DICOMGO_INTEGRATION=1` and the relevant Orthanc variables from `docs/INTEROP_ORTHANC.md`.

## Commit & Pull Request Guidelines

Recent commits use concise, imperative, sentence-case subjects such as `Support lazy dataset value streaming`. Keep commits scoped to one behavior or documentation change. Before opening a PR, run `make check`, describe user-visible impact, link the relevant issue, and call out any intentional DICOM-standard limitation or conformance change. Include screenshots only for viewer or UI-facing changes.

## Security & Configuration Tips

The network stack is intended for trusted networks unless callers explicitly configure transport and deployment policy. Do not expose the example SCP/SCU tools or future DICOMweb helpers directly to untrusted peers; this project does not provide authentication, authorization, credential storage, or audit-log retention.
