"""Generate synthetic JPEG-LS candidates with pinned CharLS; never approve goldens.

No DICOM/PHI input, network or Python codec plugins. The CharLS C API receives
planar samples for ILV=0 and RGB triplets for ILV=1/2 (its line adapter rearranges
triplets internally). Decode each stream and compare the entire source first.
"""
import argparse
import ctypes as c
import hashlib
import json
from pathlib import Path
import struct
import sys

COMMIT = "36dd3307e070d8fbc765c3ba890b7e681046fa39"
SOURCE = "https://github.com/team-charls/charls/tree/" + COMMIT
GENERATOR = "scripts/generate-jpegls-interleave.py v1"


class Frame(c.Structure):
    _fields_ = [("width", c.c_uint32), ("height", c.c_uint32),
                ("bits", c.c_int32), ("components", c.c_int32)]


class Preset(c.Structure):
    _fields_ = [(name, c.c_int32) for name in ("maxval", "t1", "t2", "t3", "reset")]


def main():
    parser = argparse.ArgumentParser(__doc__)
    parser.add_argument("--charls", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--near-lossless", action="store_true", help="generate the separate .81 source/error-bound corpus")
    args = parser.parse_args()
    if sys.byteorder != "little":
        raise RuntimeError("requires little-endian host")
    lib = c.CDLL(str(args.charls.resolve(strict=True)))
    lib.charls_get_version_string.restype = c.c_char_p
    if lib.charls_get_version_string() != b"2.4.2":
        raise RuntimeError("requires CharLS 2.4.2")
    for kind in ("encoder", "decoder"):
        signatures = [
            ("create", [], c.c_void_p), ("destroy", [c.c_void_p], None),
        ]
        if kind == "encoder":
            signatures += [
                ("set_frame_info", [c.c_void_p, c.POINTER(Frame)], c.c_int),
                ("set_interleave_mode", [c.c_void_p, c.c_int], c.c_int),
                ("set_near_lossless", [c.c_void_p, c.c_int], c.c_int),
                ("set_preset_coding_parameters", [c.c_void_p, c.POINTER(Preset)], c.c_int),
                ("get_estimated_destination_size", [c.c_void_p, c.POINTER(c.c_size_t)], c.c_int),
                ("set_destination_buffer", [c.c_void_p, c.c_void_p, c.c_size_t], c.c_int),
                ("encode_from_buffer", [c.c_void_p, c.c_void_p, c.c_size_t, c.c_uint32], c.c_int),
                ("get_bytes_written", [c.c_void_p, c.POINTER(c.c_size_t)], c.c_int),
            ]
        else:
            signatures += [
                ("set_source_buffer", [c.c_void_p, c.c_void_p, c.c_size_t], c.c_int),
                ("read_header", [c.c_void_p], c.c_int),
                ("get_destination_size", [c.c_void_p, c.c_uint32, c.POINTER(c.c_size_t)], c.c_int),
                ("decode_to_buffer", [c.c_void_p, c.c_void_p, c.c_size_t, c.c_uint32], c.c_int),
            ]
        for name, argtypes, restype in signatures:
            fn = getattr(lib, "charls_jpegls_" + kind + "_" + name)
            fn.argtypes, fn.restype = argtypes, restype

    def call(kind, name, *values):
        result = getattr(lib, "charls_jpegls_" + kind + "_" + name)(*values)
        if name not in ("create", "destroy") and result != 0:
            raise RuntimeError(f"CharLS {kind}/{name}: {result}")
        return result

    args.output.mkdir(parents=True, exist_ok=True)
    fixtures = []
    profiles = [(bits, 129, 33, None) for bits in range(2, 17)]
    profiles += [(8, 1, 9, None), (8, 9, 1, None),
                 (8, 129, 33, Preset(255, 4, 9, 28, 64)),
                 (16, 129, 33, Preset(65535, 18, 67, 276, 64))]
    profiles = [(*profile, 0, 3) for profile in profiles]
    if args.near_lossless:
        profiles = [(bits, 129, 33, None, 1, components)
                    for bits in range(2, 17) for components in (1, 3)]
        profiles += [(bits, 129, 33, None, near, components)
                     for bits in (8, 12, 16) for near in (3, 7, min(255, ((1 << bits)-1)//2))
                     for components in (1, 3)]
        profiles += [(bits, 129, 33, Preset((1 << bits)-1, 5, 17, 41, 64), 3, components)
                     for bits in (8, 16) for components in (1, 3)]
        profiles += [(bits, 129, 33, None, 0, components)
                     for bits in (8, 16) for components in (1, 3)]
        profiles += [(8, 9, 1, None, 1, components) for components in (1, 3)]
    folder = "jpegls-near/" if args.near_lossless else "jpegls/"
    generator = GENERATOR + (" near-lossless extension v1" if args.near_lossless else "")
    for bits, width, height, preset, near, components in profiles:
        maximum = preset.maxval if preset else (1 << bits) - 1
        samples = []
        for y in range(height):
            for x in range(width):
                for component in range(components):
                    # Distinct channel runs, shared runs, changing Rb at run
                    # interruptions, extremes and deterministic high variation.
                    if y < 4:
                        value = 0 if x < (13 + component * 19) else maximum
                    elif y < 10:
                        value = ((y // 2) % 3) * (maximum // 3)
                    elif y < 18:
                        value = ((x // (3 + component * 7)) % 2) * maximum
                    else:
                        value = ((x * 1103515245 + y * 12345 + component * 2654435761) ^ (x * y * 7919)) % (maximum + 1)
                    samples.append(value)
        pack = lambda values: b"".join(struct.pack("<B" if bits <= 8 else "<H", v) for v in values)
        raw = pack(samples)
        base = f"rgb{bits}-{width}x{height}" + ("-preset" if preset else "")
        if args.near_lossless:
            base = f"near-{near}-{components}c-{bits}-{width}x{height}" + ("-preset" if preset else "")
        original_name = base + (".source.raw" if args.near_lossless else ".raw")
        (args.output / original_name).write_bytes(raw)
        for ilv in (range(3) if components == 3 else [0]):
            print(f"Checking {base} ILV={ilv}", flush=True)
            arranged = pack([samples[i] for channel in range(components) for i in range(channel, len(samples), components)]) if ilv == 0 else raw
            encoder = call("encoder", "create")
            decoder = call("decoder", "create")
            if not encoder or not decoder:
                raise RuntimeError("CharLS allocation failed")
            try:
                call("encoder", "set_frame_info", encoder, c.byref(Frame(width, height, bits, components)))
                call("encoder", "set_interleave_mode", encoder, ilv)
                call("encoder", "set_near_lossless", encoder, near)
                if preset:
                    call("encoder", "set_preset_coding_parameters", encoder, c.byref(preset))
                size = c.c_size_t()
                call("encoder", "get_estimated_destination_size", encoder, c.byref(size))
                encoded = c.create_string_buffer(size.value)
                call("encoder", "set_destination_buffer", encoder, encoded, size.value)
                source = c.create_string_buffer(arranged)
                call("encoder", "encode_from_buffer", encoder, source, len(arranged), 0)
                call("encoder", "get_bytes_written", encoder, c.byref(size))
                stream = encoded.raw[:size.value]
                call("decoder", "set_source_buffer", decoder, encoded, size.value)
                call("decoder", "read_header", decoder)
                call("decoder", "get_destination_size", decoder, 0, c.byref(size))
                if size.value != len(arranged):
                    raise RuntimeError("wrong reconstruction size")
                decoded = c.create_string_buffer(size.value)
                call("decoder", "decode_to_buffer", decoder, decoded, size.value, 0)
                if not args.near_lossless and decoded.raw != arranged:
                    offset = next(i for i, (a, b) in enumerate(zip(decoded.raw, arranged)) if a != b)
                    raise RuntimeError(f"independent reconstruction differs: {base} ILV={ilv}, byte {offset}: {decoded.raw[offset]} != {arranged[offset]}")
            finally:
                call("encoder", "destroy", encoder)
                call("decoder", "destroy", decoder)
            name = f"{base}-ilv{ilv}.jls"
            (args.output / name).write_bytes(stream)
            reconstructed = [v[0] for v in struct.iter_unpack("<B" if bits <= 8 else "<H", decoded.raw)]
            if ilv == 0 and components > 1:
                reconstructed = [reconstructed[channel*width*height+i] for i in range(width*height) for channel in range(components)]
            if any(abs(a-b) > near for a, b in zip(reconstructed, samples)):
                raise RuntimeError("CharLS reconstruction violates source NEAR bound: " + name)
            expected = pack(reconstructed)
            reference_name = f"{base}-ilv{ilv}.raw" if args.near_lossless else base+".raw"
            (args.output / reference_name).write_bytes(expected)
            layout = dict(rows=height, columns=width, components=components, bitsAllocated=8 if bits <= 8 else 16,
                          bitsStored=bits, highBit=bits-1, signed=False, planar=False, bigEndian=False)
            fixtures.append(dict(
                id="jpegls-" + name[:-4], family="jpeg-ls", path=folder+name,
                sha256=hashlib.sha256(stream).hexdigest(), referencePath=folder+reference_name,
                referenceSha256=hashlib.sha256(expected).hexdigest(), comparison="exact-reconstruction",
                modality="OT", bitsAllocated=layout["bitsAllocated"], signed=False, color=components>1,
                multiframe=False, lossy=near>0, generator=generator, expectation="decode-success",
                baselineSkip="", qualificationLimit=f"{'RGB' if components == 3 else 'MONOCHROME'} unsigned, NEAR={near}, ILV={ilv}",
                input=dict(transferSyntax="1.2.840.10008.1.2.4.81" if args.near_lossless else "1.2.840.10008.1.2.4.80", frames=1, rows=height,
                           columns=width, components=components, bitsAllocated=layout["bitsAllocated"],
                           bitsStored=bits, highBit=bits-1, signed=False, photometric="RGB" if components == 3 else "MONOCHROME2", planarConfiguration=0),
                reconstruction=dict(layout={dict(bitsAllocated="bits_allocated", bitsStored="bits_stored", highBit="high_bit", bigEndian="big_endian").get(k, k): v for k, v in layout.items()}, frames=1, backend="CharLS C API", version="2.4.2",
                                    source=SOURCE, generator=generator),
                provenance=dict(Source=SOURCE, Synthetic=True, NoPHI=True, License="MIT",
                                Permission="Project-authored synthetic data; redistributable",
                                Notes="CharLS BSD-3-Clause tool; no tool code embedded; all samples checked against source")))
            if args.near_lossless:
                fixtures[-1]["sourceSamples"] = dict(path=folder+original_name, sha256=hashlib.sha256(raw).hexdigest(), near=near)
    record = dict(charlsCommit=COMMIT, charlsVersion="2.4.2",
                  charlsBinarySHA256=hashlib.sha256(args.charls.read_bytes()).hexdigest(), fixtures=fixtures)
    (args.output / "generation.json").write_text(json.dumps(record, indent=2)+"\n", encoding="utf-8")
    print(f"Generated and independently reconstructed {len(fixtures)} candidate streams")


if __name__ == "__main__":
    main()
