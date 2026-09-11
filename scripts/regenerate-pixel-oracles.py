"""Explicit local regeneration of approved full-frame references (never downloads).

Requires pydicom 3.0.2 and CharLS 2.4.2. pydicom is only the DICOM container
reader for JPEG-LS; decoding goes directly to the pinned CharLS C API. RLE uses
pydicom's pure Python decoder, not a codec shared with the Go implementation.
Writes candidates into a caller-selected directory; does not approve goldens.
"""
import argparse
import ctypes as c
import hashlib
import json
from pathlib import Path
import sys

import pydicom
import numpy
from pydicom.encaps import generate_frames


def main():
    parser = argparse.ArgumentParser(__doc__)
    parser.add_argument("--charls", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if pydicom.__version__ != "3.0.2" or numpy.__version__ != "2.1.3" or sys.byteorder != "little":
        raise RuntimeError("requires pydicom 3.0.2, numpy 2.1.3 on little-endian host")
    lib = c.CDLL(str(args.charls.resolve(strict=True)))
    lib.charls_get_version_string.restype = c.c_char_p
    if lib.charls_get_version_string() != b"2.4.2":
        raise RuntimeError("requires CharLS 2.4.2")
    for name, argtypes, restype in [
        ("create", [], c.c_void_p),
        ("destroy", [c.c_void_p], None),
        ("set_source_buffer", [c.c_void_p, c.c_void_p, c.c_size_t], c.c_int),
        ("read_header", [c.c_void_p], c.c_int),
        ("get_destination_size", [c.c_void_p, c.c_uint32, c.POINTER(c.c_size_t)], c.c_int),
        ("decode_to_buffer", [c.c_void_p, c.c_void_p, c.c_size_t, c.c_uint32], c.c_int),
    ]:
        fn = getattr(lib, "charls_jpegls_decoder_" + name)
        fn.argtypes, fn.restype = argtypes, restype

    def check(code):
        if code:
            raise RuntimeError(f"CharLS error {code}")

    def decode(data):
        decoder = lib.charls_jpegls_decoder_create()
        if not decoder:
            raise RuntimeError("CharLS allocation failed")
        try:
            source = c.create_string_buffer(data)
            check(lib.charls_jpegls_decoder_set_source_buffer(decoder, source, len(data)))
            check(lib.charls_jpegls_decoder_read_header(decoder))
            size = c.c_size_t()
            check(lib.charls_jpegls_decoder_get_destination_size(decoder, 0, c.byref(size)))
            if not 0 < size.value <= 64 * 1024 * 1024:
                raise RuntimeError("oracle output exceeds bound")
            result = c.create_string_buffer(size.value)
            check(lib.charls_jpegls_decoder_decode_to_buffer(decoder, result, size.value, 0))
            return result.raw
        finally:
            lib.charls_jpegls_decoder_destroy(decoder)

    root = Path(__file__).resolve().parents[1] / "pixeldata/codecfixture/testdata/codecfull"
    manifest = json.loads((root / "manifest.json").read_text())
    args.output.mkdir(parents=True, exist_ok=True)
    records = []
    names = {"JPEGLSNearLossless_08.dcm", "JPEGLSNearLossless_16.dcm", "SC_rgb_rle_16bit_2frame.dcm"}
    for fixture in manifest["fixtures"]:
        if Path(fixture.get("path", "")).name not in names:
            continue
        path = root / fixture["path"]
        if hashlib.sha256(path.read_bytes()).hexdigest() != fixture["sha256"]:
            raise RuntimeError("input fixture hash mismatch: " + fixture["id"])
        ds = pydicom.dcmread(path)
        if fixture["family"] == "jpeg-ls":
            raw = b"".join(decode(frame) for frame in generate_frames(ds.PixelData, number_of_frames=int(ds.get("NumberOfFrames", 1))))
            backend = "CharLS 2.4.2 (direct C API)"
        else:
            ds.pixel_array_options(decoding_plugin="pydicom", raw=True)
            raw = ds.pixel_array.astype("<u2", copy=False).tobytes()
            backend = "pydicom 3.0.2 pure Python RLE"
        expected_size = int(ds.Rows) * int(ds.Columns) * int(ds.SamplesPerPixel) * (int(ds.BitsAllocated) // 8) * int(ds.get("NumberOfFrames", 1))
        if len(raw) != expected_size:
            raise RuntimeError("unexpected full reconstruction length")
        name = path.stem + ".raw"
        (args.output / name).write_bytes(raw)
        records.append({"id": fixture["id"], "file": name, "sha256": hashlib.sha256(raw).hexdigest(), "backend": backend, "bytes": len(raw)})
    if len(records) != 3:
        raise RuntimeError("required oracle fixtures missing")
    (args.output / "generation.json").write_text(json.dumps({"pydicom": pydicom.__version__, "numpy": numpy.__version__, "charlsCommit": "36dd3307e070d8fbc765c3ba890b7e681046fa39", "charlsBinarySHA256": hashlib.sha256(args.charls.read_bytes()).hexdigest(), "records": records}, indent=2) + "\n")
    print(json.dumps(records, indent=2))


if __name__ == "__main__":
    main()
