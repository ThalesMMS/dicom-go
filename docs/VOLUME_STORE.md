# Canonical VolumeStore

`render.VolumeStore` is the ownership boundary between dicom-go ingest and CPU,
WebGPU or native render engines. It implements the frozen VolumeSnapshot V1
contract in DICOM patient LPS millimetres.

## Ownership model

- A `Stack` owns references to caller-owned `Frame.PixelBytes`. Those raw
  frames remain available to the independent 2D path and are not silently
  freed when a 3D volume is evicted.
- The first regular-volume sampler decodes one tightly packed `float32`
  modality allocation. `VolumeStore` owns that allocation. MPR and VR acquire
  read leases on the same generation; there is no second per-renderer voxel
  cache.
- `Volume.AcquireReader` holds one lease for an entire CPU render and supports
  concurrent read-only sampling. Pixel loops must not acquire a snapshot per
  sample; the reader is closed only after all rendering workers have joined.
- Irregular spacing, gaps and stable gantry tilt are decoded into a transient
  source buffer and regularized by dicom-go. Only the resulting regular affine
  grid is returned by `Volume.AcquireSnapshot`. It is a new generation whose
  descriptor records the source generation as `ParentGeneration`.
- `VolumeSnapshot` exposes a value-only descriptor and scalar/read-copy
  methods. It never exposes a writable voxel slice. Native and GPU transports
  copy through `WritePayloadTo`; they must not retain a Go pointer.
- RGBA render buffers, raw-frame ownership transfers and backend/native copies
  can be attached with `TrackMemory`. The returned memory lease is the owner of
  that accounting entry and must be released only when the real allocation is
  no longer live.
- VOI, LUT, window/level, transfer functions, cameras and other presentation
  state are not fields of `VolumeSnapshot` and do not advance its generation.

`Stack.Close`, `Volume.Close`, `Series.CloseVolumeStore` and
`Study.CloseVolumeStores` retire normalized generations. A renderer that
already holds a lease remains valid; the last lease or pin release performs the
actual reclamation. These methods and lease releases are idempotent.

## Exact byte accounting

`VolumeStoreStats.LiveBytes` is the exact sum of:

- store-owned voxel backing arrays, using their readable allocation length;
- explicitly tracked live raw, RGBA and backend-copy allocations.

`ByKind` separates `raw_frames`, `normalized_volume`,
`regularized_volume`, `rgba_render_buffer` and `backend_copy`.
`ActiveLeases` and `ActivePins` expose the exact reference counts; byte totals
count each protected allocation once rather than multiplying it by readers.

The number deliberately excludes Go map buckets, mutexes, interface headers,
descriptors and lease objects. `MetadataRecords` and `TrackedRecords` expose the
counts needed to bound that implementation overhead without pretending it is
pixel residency. Go allocator size-class slack and runtime bookkeeping are also
outside payload accounting and must be measured with heap/RSS profiles.

`MaxLiveBytes == 0` means no hard ceiling. A non-zero ceiling is an admission
limit: replacement or external allocation fails with
`ErrVolumeBudgetExceeded` if safe retired-generation eviction cannot make it
fit. A failed admission does not replace the current generation. Leased or
pinned bytes are never reused or evicted. Applications install such a store
before the first stack volume build with `Stack.SetVolumeStore`.

## Lifecycle rules

1. Acquire a `VolumeLease`, or a `VolumeReader` for repeated CPU sampling, and
   retain it for the complete render or upload.
2. Read the descriptor and samples only through its `VolumeSnapshot`.
3. Attach every independently allocated native/GPU/RGBA copy with
   `TrackMemory`; do not count aliases twice.
4. Release copy leases when those allocations are actually freed.
5. Release the volume lease after the renderer has stopped reading.
6. Close the series/study owner during replacement or viewer shutdown.

The store starts no goroutines. Release callbacks run after its mutex is
unlocked, so callbacks may safely query telemetry or enter backend teardown.
