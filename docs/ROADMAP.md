# Repository outcome roadmap

This roadmap defines stable outcome and dependency ordering for `dicom-go` and
Twin-Viewer. It deliberately does not copy issue state, release history, or the
current capability inventory.

Use the [current capability and limitation scope](CAPABILITIES.md) to decide
what the library supports. Use the
[live open-issue query](https://github.com/ThalesMMS/go-dev/issues?q=is%3Aissue%20state%3Aopen)
for executable work and [Epic #899](https://github.com/ThalesMMS/go-dev/issues/899)
for the current cross-repository outcome grouping.

## Outcome dependency model

| Outcome | Requires | Evidence before downstream work depends on it |
|---|---|---|
| Correct, bounded DICOM primitives | No higher layer | Public API tests, malformed-input tests, resource budgets, and the affected `dicom-go` quality gates |
| Reusable protocol, pixel, render, ROI, and clinical services | Correct lower-level data, parser, object, transfer, and geometry contracts | Package-local tests, explicit ownership and cancellation, security boundaries, and current capability documentation |
| Product archive, network, export, settings, and viewer workflows | Reusable library services with no UI dependency | Shared Twin service tests, durable state/error contracts, and architecture checks |
| Independent Mac-style and Windows-style experiences | Shared product workflows and presentation-neutral models | Cross-style behavior tests plus shell-specific accessibility and lifecycle tests |
| Release qualification | Every changed layer above | Offline module checks, required conformance/codec gates, synthetic non-PHI fixtures, and opt-in interoperability evidence where applicable |

Work moves from a lower row to a higher row only when the lower-layer contract
is reusable without importing its consumer. A UI issue that exposes an existing
service may start at the shell row; a protocol or clinical correctness gap must
start in `dicom-go` and flow upward. Measurement, discovery, or qualification
work precedes an implementation promise when performance, interoperability, or
clinical accuracy is not yet evidenced.

## Sequencing live work

1. The issue body identifies the observable outcome and the lowest owning
   layer.
2. Dependencies reference live issues and point only to earlier prerequisite
   work.
3. Library behavior is accepted before application adapters; shared application
   behavior is accepted before either shell duplicates it.
4. The affected module gates pass before a downstream issue treats the outcome
   as available.
5. Shipped behavior moves into capability or user documentation; the issue and
   Epic retain operational history.

## Maintenance rule

This file may describe stable outcomes and dependency rules, but may not embed
an issue inventory. No versioned Markdown document may contain a generated
issue list; generated inventories are limited to transient CI or release
artifacts. Epic #899 is the only curated cross-repository issue inventory, and
the live GitHub query is authoritative for open/closed state.

Issue citations in architecture decisions, qualification records, and design
documents are traceability links, not backlog status. Current capability claims
must point to code/tests or [CAPABILITIES.md](CAPABILITIES.md), never to a closed
issue or milestone snapshot.
