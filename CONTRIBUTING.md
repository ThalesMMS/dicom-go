# Contributing to dicom-go

Contributions should keep the repository testable and predictable before expanding DICOM coverage.

## Local workflow

Use Go 1.22 or newer and run the standardized targets from the repository root:

```sh
make fmt
make vet
make test
make build
make check
```

`make check` is the default pre-PR workflow and runs formatting, vet, tests,
builds, offline documentation validation, and codec policy checks.
See the [documentation validation guide](docs/DOCUMENTATION_VALIDATION.md) for
the checked corpus, explicit exemptions, and safe command policy.

## Formatting requirements

- Always run `gofmt` before committing.
- Prefer `make fmt` as the standard local command.
- Prefer `make fmt-check` as the pre-PR formatting verification command to mirror CI.
- Running `gofmt -w .` directly is acceptable when needed.

## Testing requirements

- `go test ./...` must pass before submitting a pull request.
- Prefer `make test` as the standard local command.
- Add or update tests when changing behavior.

## Static analysis

- `go vet ./...` should report no issues before opening a pull request.
- Prefer `make vet` as the standard local command.
- Run `make check` before submitting when you want the full local baseline.

## Naming conventions

- Use lower-case package names without underscores.
- Follow standard Go idioms for mixedCaps naming.
- Use descriptive exported identifiers in `PascalCase` and unexported helpers in `camelCase`.
- Name tests with `TestXxx` in `*_test.go`.

## Pull requests

- Include a brief summary of the change, its validation, and a link to the
  relevant issue in the pull request description.
- Complete the interface-governance checklist whenever a change affects contracts, fingerprints, checkpoints, or exported summaries.
- Document any intentional design decisions or divergences from the DICOM standard in the pull request description.
- Avoid introducing new DICOM functionality in infra-only issues.
