# JPEG XL production helper (#924)

Status: opt-in backend. The default library profile remains pure Go. `Register()`
still installs the `djxl` CLI decoder.

Issue `#923` chose **recommend-helper** from the `examples/codec-cost` prototype
(`jpegxl-helper-proto`). That prototype is not this worker. This branch
implements the production helper:

| Layer | Role |
| --- | --- |
| Prototype | `examples/codec-cost` measurement harness (`CST1`) |
| Opt-in backend | `examples/codec-adapters/jpegxl` `Session` + `jpegxl-helper` (`JXLH` v1) |
| Promoted profile | still `jpegxl_djxl` / `codecfull`; helper is additional, not required |

## Supervision

- Twin owns one helper session per process when `JPEGXLAdapterEnabled()` and a
  `jpegxl-helper` binary is packaged or `DICOM_GO_JPEGXL_HELPER` is set.
- IPC is local, private, versioned, length-prefixed stdio. Not HTTP.
- One worker. `JXL_NUM_THREADS=1`. Close is idempotent. Unix kills the process
  group; Windows uses `WaitDelay`.
- `DecodeFrameContext` from `#922` is preserved. Cancel/timeout is never
  classified as a malformed codestream. In-flight cancel resets only that
  worker; queued requests keep their request IDs.
- Crash, hang, truncated reply, swapped IDs, oversized payloads, and channel
  loss do not publish pixels. The Twin process stays up. Fallback to `djxl` is
  explicit and pixel-equivalent when the CLI is available.
- Helper RSS and in-flight IPC/native buffers are admitted as `KindCodec`
  against the Twin memory budget.

## Qualification on this host

Recorded 2026-09-09 on darwin/arm64 with `libjxl`/`djxl` 0.12.0 (codecfull pin
remains 0.11.2).

- `TestProductionHelperDecodesCodecfullGray16WhenLibjxlAvailable` passed
  (codecfull `jxl/gray16-lossless.jxl`, 64×64×16-bit).
- `TestProductionHelperPixelsMatchDjxlCLI` passed: helper native little-endian
  pixels matched `djxl` CLI output byte-for-byte.
- Isolation harness: crash, hang, truncated reply, swapped IDs, oversized
  payloads, and channel loss do not publish frames; the caller process continues.
- `#923` already showed helper first-frame p95 below CLI on `jpegxl-gray16-warm`
  and `jpegxl-series-switch`. This issue does not re-run that campaign.

PACS `HOROS` at `192.168.100.62:4007` was unreachable and unused.
