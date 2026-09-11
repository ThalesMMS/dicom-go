"""Independent synthetic archive peer; pydicom 3.0.2 / pynetdicom 3.0.4."""
import sys
import os
from pathlib import Path
import numpy as np
import pydicom
import pynetdicom
from pydicom.dataset import Dataset, FileMetaDataset
from pydicom.uid import ExplicitVRLittleEndian
from pynetdicom import AE, evt, build_role
if os.environ.get("DICOMGO_PEER_DEBUG"):
    from pynetdicom import debug_logger
    debug_logger()
from pynetdicom.sop_class import (
    SecondaryCaptureImageStorage as SC,
    StudyRootQueryRetrieveInformationModelFind as FIND,
    StudyRootQueryRetrieveInformationModelGet as GET,
    StudyRootQueryRetrieveInformationModelMove as MOVE,
    StorageCommitmentPushModel as COMMIT,
)

assert pydicom.__version__ == "3.0.2" and pynetdicom.__version__ == "3.0.4"
root, phase = Path(sys.argv[1]), int(sys.argv[2])
prefix = "1.2.826.0.1.3680043.10.914"
expected = {}
for i in range(6):
    ds = Dataset()
    ds.file_meta = FileMetaDataset()
    ds.file_meta.TransferSyntaxUID = ExplicitVRLittleEndian
    ds.SOPClassUID = SC
    ds.SOPInstanceUID = f"{prefix}.9.{i + 1}"
    ds.StudyInstanceUID = f"{prefix}.{i // 3 + 1}"
    ds.SeriesInstanceUID = f"{ds.StudyInstanceUID}.{i % 3 // 2 + 1}"
    ds.PatientID = "SYNTHETIC"
    ds.PatientName = "SYNTHETIC^ONLY"
    ds.StudyDate = "20260907"
    ds.Modality = "OT"
    ds.InstanceNumber = str(i + 1)
    ds.Rows, ds.Columns, ds.NumberOfFrames = 17, 19, "2"
    ds.SamplesPerPixel = 1
    ds.PhotometricInterpretation = "MONOCHROME2"
    ds.BitsAllocated, ds.BitsStored, ds.HighBit, ds.PixelRepresentation = 16, 12, 11, 0
    samples = ((np.arange(646, dtype=np.uint32) * 13 + i * 101) % 4096).astype("<u2")
    ds.PixelData = samples.tobytes()
    expected[str(ds.SOPInstanceUID)] = (ds, samples.reshape(2, 17, 19))

failures, got_get, got_move = [], [], []
cancel_after_store = False
def check(ds):
    original, samples = expected[str(ds.SOPInstanceUID)]
    for keyword in ["SOPClassUID", "SOPInstanceUID", "StudyInstanceUID", "SeriesInstanceUID",
                    "PatientID", "PatientName", "StudyDate", "Modality", "InstanceNumber",
                    "Rows", "Columns", "NumberOfFrames", "SamplesPerPixel",
                    "PhotometricInterpretation", "BitsAllocated", "BitsStored", "HighBit", "PixelRepresentation"]:
        assert getattr(ds, keyword) == getattr(original, keyword), keyword
    assert ds.PixelData == original.PixelData
    assert np.array_equal(ds.pixel_array, samples)

def receive(bucket):
    def on_store(event):
        global cancel_after_store
        try:
            ds = event.dataset
            ds.file_meta = event.file_meta
            check(ds)
            assert event.request.AffectedSOPInstanceUID == ds.SOPInstanceUID
            bucket.append(str(ds.SOPInstanceUID))
            if cancel_after_store and bucket is got_get:
                cancel_after_store = False
                assoc.send_c_cancel(91, query_model=GET)
            return 0x0000
        except Exception as exc:
            failures.append(type(exc).__name__)
            return 0xA700
    return on_store

move_peer = AE(ae_title="MOVEDEST")
move_peer.add_supported_context(SC, ExplicitVRLittleEndian)
move_server = move_peer.start_server(("127.0.0.1", 0), block=False, evt_handlers=[(evt.EVT_C_STORE, receive(got_move))])
print("PORT", move_server.server_address[1], flush=True)
archive_port = int(sys.stdin.readline())
ae = AE(ae_title="SYNTHETIC")
ae.acse_timeout = ae.dimse_timeout = ae.network_timeout = 5
for sop in [SC, FIND, GET, MOVE, COMMIT]:
    ae.add_requested_context(sop, ExplicitVRLittleEndian)
assoc = ae.associate("127.0.0.1", archive_port, ae_title="REFERENCEARCHIVE",
                     ext_neg=[build_role(SC, scu_role=True, scp_role=True)],
                     evt_handlers=[(evt.EVT_C_STORE, receive(got_get))])
assert assoc.is_established
assert all(str(c.abstract_syntax) != str(COMMIT) for c in assoc.accepted_contexts)
try:
    if phase == 0:
        for ds, _ in expected.values():
            assert assoc.send_c_store(ds).Status == 0
        assert assoc.send_c_store(next(iter(expected.values()))[0]).Status == 0xA900
    query = Dataset()
    query.QueryRetrieveLevel = "STUDY"
    query.StudyInstanceUID = ""
    query.PatientName = "SYNTH*"
    matches = list(assoc.send_c_find(query, FIND))
    assert matches[-1][0].Status == 0
    assert {str(ds.StudyInstanceUID) for status, ds in matches if status.Status in [0xFF00, 0xFF01]} == {f"{prefix}.1", f"{prefix}.2"}
    for study in [1, 2]:
        query = Dataset()
        query.QueryRetrieveLevel = "STUDY"
        query.StudyInstanceUID = f"{prefix}.{study}"
        results = list(assoc.send_c_get(query, GET))
        assert results[-1][0].Status == 0 and results[-1][0].NumberOfCompletedSuboperations == 3
        results = list(assoc.send_c_move(query, "MOVEDEST", MOVE))
        assert results[-1][0].Status == 0 and results[-1][0].NumberOfCompletedSuboperations == 3
    denied = list(assoc.send_c_move(query, "NOT_ALLOWED", MOVE))
    assert denied[-1][0].Status == 0xA801
    assert sorted(got_get) == sorted(expected) and sorted(got_move) == sorted(expected)
    assert not failures
    # Cancel while the archive is waiting for a real C-STORE acknowledgement.
    # The C-CANCEL precedes that response on the same association, avoiding a
    # timing race with a completed three-instance retrieval.
    cancel_after_store = True
    canceled = list(assoc.send_c_get(query, GET, msg_id=91))
    assert canceled[-1][0].Status == 0xFE00
    final = canceled[-1][0]
    assert sum(int(getattr(final, key, 0)) for key in ["NumberOfRemainingSuboperations", "NumberOfCompletedSuboperations", "NumberOfFailedSuboperations", "NumberOfWarningSuboperations"]) == 3
    assert not failures
    files = sorted(root.glob("*.dcm"))
    assert len(files) == 6
    for path in files:
        ds = pydicom.dcmread(path)
        assert ds.file_meta.MediaStorageSOPClassUID == ds.SOPClassUID
        assert ds.file_meta.MediaStorageSOPInstanceUID == ds.SOPInstanceUID
        assert ds.file_meta.TransferSyntaxUID == ExplicitVRLittleEndian
        check(ds)
    print("VERIFIED", phase, "6 files; 3876 samples per store/get/move", flush=True)
finally:
    assoc.release()
    move_server.shutdown()
