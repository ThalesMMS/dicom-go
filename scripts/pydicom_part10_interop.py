#!/usr/bin/env python3
"""Independent, synthetic Part 10 interoperability oracle for dicom-go.

The Go opt-in test owns orchestration and temporary files:

1. Go writes ``go-<case>.dcm`` and runs ``prepare ROOT``.
2. ``prepare`` validates those files and independently authors
   ``pydicom-<case>.dcm``.
3. Go reads each pydicom file and writes ``go-rewrite-<case>.dcm``.
4. Go runs ``verify-rewrites ROOT`` to check the rewritten semantics.

The fixtures use deterministic synthetic identifiers and values. They contain
no patient data and are created only in the caller-provided temporary directory.
"""

from __future__ import annotations

import argparse
import struct
import sys
from dataclasses import dataclass
from pathlib import Path

import pydicom
from pydicom.dataset import Dataset, FileDataset, FileMetaDataset
from pydicom.sequence import Sequence
from pydicom.uid import UID


REQUIRED_PYDICOM_VERSION = "3.0.2"
SECONDARY_CAPTURE_SOP_CLASS_UID = "1.2.840.10008.5.1.4.1.1.7"
IMPLEMENTATION_CLASS_UID = "1.2.826.0.1.3680043.10.543.867.99"
UID_ROOT = "1.2.826.0.1.3680043.10.543.867"
EXPLICIT_VR_LITTLE_ENDIAN = "1.2.840.10008.1.2.1"
IMPLICIT_VR_LITTLE_ENDIAN = "1.2.840.10008.1.2"
EXPLICIT_VR_BIG_ENDIAN = "1.2.840.10008.1.2.2"
PIXEL_VALUES = (1, 256, 4095, 65535, 42, 1024)


@dataclass(frozen=True)
class InteropCase:
    name: str
    transfer_syntax_uid: str
    uid_suffix: int
    undefined_sequence: bool = False

    @property
    def study_instance_uid(self) -> str:
        return f"{UID_ROOT}.{self.uid_suffix}.1"

    @property
    def series_instance_uid(self) -> str:
        return f"{UID_ROOT}.{self.uid_suffix}.2"

    @property
    def sop_instance_uid(self) -> str:
        return f"{UID_ROOT}.{self.uid_suffix}.3"

    @property
    def referenced_sop_instance_uid(self) -> str:
        return f"{UID_ROOT}.{self.uid_suffix}.4"

    @property
    def marker(self) -> str:
        return f"SYNTHETIC INTEROP {self.name}"


CASES = (
    InteropCase("explicit-little", EXPLICIT_VR_LITTLE_ENDIAN, 1),
    InteropCase("implicit-little", IMPLICIT_VR_LITTLE_ENDIAN, 2),
    InteropCase("explicit-big", EXPLICIT_VR_BIG_ENDIAN, 3),
    InteropCase("undefined-sequence", EXPLICIT_VR_LITTLE_ENDIAN, 4, True),
    InteropCase("native-pixel", EXPLICIT_VR_LITTLE_ENDIAN, 5),
)


def require_pinned_pydicom() -> None:
    if sys.flags.optimize != 0:
        raise RuntimeError(
            "Python optimization must be disabled because it removes interoperability checks"
        )
    if pydicom.__version__ != REQUIRED_PYDICOM_VERSION:
        raise RuntimeError(
            f"pydicom {REQUIRED_PYDICOM_VERSION} is required; "
            f"found {pydicom.__version__}"
        )


def transfer_encoding(case: InteropCase) -> tuple[bool, bool]:
    if case.transfer_syntax_uid == IMPLICIT_VR_LITTLE_ENDIAN:
        return True, True
    if case.transfer_syntax_uid == EXPLICIT_VR_LITTLE_ENDIAN:
        return True, False
    if case.transfer_syntax_uid == EXPLICIT_VR_BIG_ENDIAN:
        return False, False
    raise AssertionError(f"unqualified transfer syntax in matrix: {case.name}")


def build_independent_dataset(path: Path, case: InteropCase) -> FileDataset:
    little_endian, implicit_vr = transfer_encoding(case)
    file_meta = FileMetaDataset()
    file_meta.FileMetaInformationVersion = b"\x00\x01"
    file_meta.MediaStorageSOPClassUID = UID(SECONDARY_CAPTURE_SOP_CLASS_UID)
    file_meta.MediaStorageSOPInstanceUID = UID(case.sop_instance_uid)
    file_meta.TransferSyntaxUID = UID(case.transfer_syntax_uid)
    file_meta.ImplementationClassUID = UID(IMPLEMENTATION_CLASS_UID)

    dataset = FileDataset(str(path), {}, file_meta=file_meta, preamble=b"\x00" * 128)
    dataset.SOPClassUID = UID(SECONDARY_CAPTURE_SOP_CLASS_UID)
    dataset.SOPInstanceUID = UID(case.sop_instance_uid)
    dataset.StudyInstanceUID = UID(case.study_instance_uid)
    dataset.SeriesInstanceUID = UID(case.series_instance_uid)
    dataset.Modality = "OT"
    dataset.SpecificCharacterSet = "ISO_IR 192"
    dataset.PatientName = "SYNTHETIC^MÜLLER"
    dataset.PatientID = "NO-PHI-867"
    dataset.ImageComments = case.marker

    reference = Dataset()
    reference.ReferencedSOPClassUID = UID(SECONDARY_CAPTURE_SOP_CLASS_UID)
    reference.ReferencedSOPInstanceUID = UID(case.referenced_sop_instance_uid)
    if case.undefined_sequence:
        reference.is_undefined_length_sequence_item = True
    dataset.ReferencedImageSequence = Sequence([reference])
    if case.undefined_sequence:
        dataset[0x00081140].is_undefined_length = True

    dataset.SamplesPerPixel = 1
    dataset.PhotometricInterpretation = "MONOCHROME2"
    dataset.Rows = 2
    dataset.Columns = 3
    dataset.BitsAllocated = 16
    dataset.BitsStored = 16
    dataset.HighBit = 15
    dataset.PixelRepresentation = 0
    endian = "<" if little_endian else ">"
    dataset.PixelData = struct.pack(endian + "6H", *PIXEL_VALUES)
    dataset[0x7FE00010].VR = "OW"

    # The explicit arguments make the intended transfer syntax independent of
    # deprecated Dataset encoding flags and pydicom's source-file state.
    return dataset


def write_independent_file(path: Path, case: InteropCase) -> None:
    little_endian, implicit_vr = transfer_encoding(case)
    dataset = build_independent_dataset(path, case)
    pydicom.dcmwrite(
        path,
        dataset,
        implicit_vr=implicit_vr,
        little_endian=little_endian,
        enforce_file_format=True,
    )


def validate_file(path: Path, case: InteropCase) -> None:
    if not path.is_file():
        raise AssertionError(f"missing expected interop file: {path.name}")
    dataset = pydicom.dcmread(path)
    transfer_syntax = str(dataset.file_meta.TransferSyntaxUID)
    assert transfer_syntax == case.transfer_syntax_uid, (
        path.name,
        transfer_syntax,
        case.transfer_syntax_uid,
    )
    assert str(dataset.file_meta.MediaStorageSOPClassUID) == SECONDARY_CAPTURE_SOP_CLASS_UID
    assert str(dataset.file_meta.MediaStorageSOPInstanceUID) == case.sop_instance_uid
    assert str(dataset.SOPClassUID) == SECONDARY_CAPTURE_SOP_CLASS_UID
    assert str(dataset.SOPInstanceUID) == case.sop_instance_uid
    assert str(dataset.StudyInstanceUID) == case.study_instance_uid
    assert str(dataset.SeriesInstanceUID) == case.series_instance_uid
    assert str(dataset.SpecificCharacterSet) == "ISO_IR 192"
    assert str(dataset.PatientName) == "SYNTHETIC^MÜLLER"
    assert str(dataset.PatientID) == "NO-PHI-867"
    assert str(dataset.ImageComments) == case.marker

    references = dataset.ReferencedImageSequence
    assert len(references) == 1
    assert str(references[0].ReferencedSOPClassUID) == SECONDARY_CAPTURE_SOP_CLASS_UID
    assert str(references[0].ReferencedSOPInstanceUID) == case.referenced_sop_instance_uid
    if case.undefined_sequence:
        sequence_element = dataset[0x00081140]
        assert sequence_element.is_undefined_length, path.name
        assert references[0].is_undefined_length_sequence_item, path.name

    assert int(dataset.SamplesPerPixel) == 1
    assert str(dataset.PhotometricInterpretation) == "MONOCHROME2"
    assert int(dataset.Rows) == 2
    assert int(dataset.Columns) == 3
    assert int(dataset.BitsAllocated) == 16
    assert int(dataset.BitsStored) == 16
    assert int(dataset.HighBit) == 15
    assert int(dataset.PixelRepresentation) == 0
    little_endian, _ = transfer_encoding(case)
    endian = "<" if little_endian else ">"
    pixels = struct.unpack(endian + "6H", bytes(dataset.PixelData))
    assert pixels == PIXEL_VALUES, (path.name, pixels)


def prepare(root: Path) -> None:
    root.mkdir(parents=True, exist_ok=True)
    for case in CASES:
        validate_file(root / f"go-{case.name}.dcm", case)
    for case in CASES:
        write_independent_file(root / f"pydicom-{case.name}.dcm", case)
        validate_file(root / f"pydicom-{case.name}.dcm", case)


def verify_rewrites(root: Path) -> None:
    for case in CASES:
        validate_file(root / f"go-rewrite-{case.name}.dcm", case)


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run the synthetic pydicom 3.0.2 Part 10 interop oracle."
    )
    parser.add_argument("command", choices=("prepare", "verify-rewrites"))
    parser.add_argument("root", type=Path)
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    require_pinned_pydicom()
    root = args.root.resolve()
    if args.command == "prepare":
        prepare(root)
    else:
        verify_rewrites(root)
    print(f"pydicom Part 10 {args.command} OK")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main(sys.argv[1:]))
    except (AssertionError, OSError, RuntimeError, ValueError) as error:
        print(f"pydicom Part 10 interop failed: {error}", file=sys.stderr)
        raise SystemExit(1) from error
