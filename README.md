# dicom-go

A Go DICOM toolkit.

This repository is intentionally small at this stage. It establishes package boundaries, public interfaces, and a minimal Part 10 reader path for uncompressed transfer syntaxes. It is not yet a complete DICOM implementation.

## Package layout

- `core`: DICOM base types: tag, VR, length, element, value.
- `dictionary`: dictionary interfaces and entries.
- `dictionary/std`: generated standard dictionary built from the versioned `internal/standard/dicom.dic` source.
- `encoding`: endian-aware primitive readers/writers.
- `transfer`: transfer syntax registry and built-in syntaxes.
- `parser`: token/data set reader layer.
- `object`: high-level DICOM object and Part 10 file reader.
- `pixeldata`: pixel data extraction/codec extension point.
- `dicomjson`: DICOM JSON extension point.
- `net/ul`: DICOM Upper Layer networking extension point.
- `cmd/dcmdump`: first CLI built on the library.

See `docs/ARCHITECTURE.md` for the full design.

## Defensive parsing

The parser and file reader expose optional defensive limits for untrusted
input:

- `parser.ReaderOptions.MaxSequenceDepth`
- `parser.ReaderOptions.MaxElements`
- `parser.ReaderOptions.MaxFragments`
- `parser.ReaderOptions.MaxElementBytes`
- `parser.ReaderOptions.MaxTotalBytes`
- `object.ReadFileOptions` exposes the same limits for Part 10 file reading

All limit fields default to `0`, which means unlimited.

Recommended starting points for untrusted input:

- `MaxSequenceDepth: 32`
- `MaxElements: 100000`
- `MaxFragments: 10000`
- `MaxElementBytes: 16 << 20`
- `MaxTotalBytes: 128 << 20`

Example:

```go
file, err := object.ReadFileWithOptions(r, object.ReadFileOptions{
	MaxSequenceDepth: 32,
	MaxElements:      100000,
	MaxFragments:     10000,
	MaxElementBytes:  16 << 20,
	MaxTotalBytes:    128 << 20,
})
```

Use stricter limits for highly constrained services, and larger limits for
trusted bulk-processing workloads.

## Getting Started

Pré-requisito:

- Go 1.22 ou superior instalado.

## Development

Use Go 1.22 e os comandos padronizados do repositório:

```sh
make fmt
make vet
make test
make build
make check
```

`make check` é o fluxo principal antes de abrir um PR. Ele executa formatação, `go vet` e `go test ./...` em sequência.

Para buildar todos os pacotes:

```sh
make build
```

ou

```sh
go build ./...
```

Para executar a suíte de testes:

```sh
make test
```

ou

```sh
go test ./...
```

Para regenerar o dicionário padrão:

```sh
go generate ./dictionary/std
```

Para rodar a baseline completa de qualidade:

```sh
make check
```

## Executando o `dcmdump`

Para inspecionar um arquivo Part 10 com o CLI atual:

```sh
go run ./cmd/dcmdump <file.dcm>
```

Saída esperada, no estágio atual do scaffold:

- Transfer Syntax UID e nome.
- Elementos do File Meta.
- Elementos do dataset em ordem de tag.

O comando também pode ser compilado com:

```sh
go build ./cmd/dcmdump
```

Para instalar o binário localmente:

```sh
go install ./cmd/dcmdump
```
