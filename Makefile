.PHONY: fmt fmt-check vet test build docs-check race leak fuzz-smoke pydicom-interop benchmark-large benchmark-compare benchstat-install codec-policy-check codec-optional-check codec-jpeg2000-profile-check codec-jpeg2000-openjpeg-check codec-jpegls-charls-check codec-jpegxl-djxl-check codec-jpegxl-helper codecfull-check codec-conformance-check codec-manifest-check codec-fixture-bench jpegxl-inventory jpegxl-inventory-verify codec-cost-measure check

JPEGXL_FIXTURE_DIR ?= ../JPEGXL-Fixture
JPEGXL_MANIFEST ?= pixeldata/codecfixture/testdata/codecs/jpegxl_manifest.json
CONCURRENCY_TEST_PACKAGES := ./net/dicomweb ./net/ul ./net/dimse ./net/telemetry ./transfer ./ups
LIFECYCLE_TEST_PACKAGES := ./net/dicomweb ./net/ul ./net/dimse ./net/telemetry ./ups
RACE_COUNT ?= 1
LEAK_COUNT ?= 10
LEAK_TIMEOUT ?= 5m
FUZZ_SMOKE_TIME ?= 5s
FUZZ_SMOKE_TIMEOUT ?= 2m
FUZZ_SMOKE_PARALLEL ?= 2
FUZZ_SMOKE_MEMORY ?= 1GiB
FUZZ_SMOKE_GOCACHE ?=
DICOMWEB_FUZZ_TARGETS ?= FuzzDICOMwebMetadataJSONInstanceRefs FuzzDICOMwebMetadataReferences FuzzWADOResponseParsing
PYDICOM_INTEROP_PYTHON ?= python3
PYDICOM_INTEROP_TIMEOUT ?= 10m
BENCH_COUNT ?= 6
BENCH_TIME ?= 250ms
BENCH_CPU ?= 1
BENCH_TIMEOUT ?= 20m
BENCHSTAT_VERSION ?= v0.0.0-20240510023725-bedb9135df6d
BENCHSTAT ?= benchstat
BENCHSTAT_INSTALL_DIR ?=
BASE_BENCH ?=
CANDIDATE_BENCH ?=
EXTERNAL_INTEROP_TEST_PATTERN ?= (Interop|Pynetdicom|Horos|Curl)
LIFECYCLE_TEST_PATTERN ?= ^Test(Server(GracefulServeAndShutdown|ShutdownTimeoutCleansActiveHandlers|ServeErrorCleansActiveHandlers)|AssociationServerShutdown|Dispatcher|AsyncSessionOperationContextSendsCancelAndDrainsFinal|SendCGetWithProgressCancellationReleasesOperationWhenConsumerStops|CMoveWithProgressCancellationReleasesOperationWhenConsumerStops|ServiceQueueClose|StoreSession(CancellationDuringPayloadAbortsAndCloses|ContinuesAfterRemoteFailureAndClosesSources)|DeliveryClosesAssociationReturnedWithDialError|FilteredSubscriptionScanCancellationRollsBack)

fmt:
	gofmt -w .

# Inspect without mutation; distinguish tool errors from unformatted files.
fmt-check:
	@unformatted="$$(gofmt -l .)"; rc=$$?; \
	if [ $$rc -ne 0 ]; then echo "gofmt failed (exit $$rc)" >&2; exit $$rc; fi; \
	if [ -n "$$unformatted" ]; then \
		echo "Files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi; \
	echo "gofmt: clean"

vet:
	go vet ./...

test:
	go test ./...

build:
	go build ./...

docs-check:
	go run ./cmd/doccheck -root .. -config .doccheck.json

# Opt-in concurrency gates intentionally exclude native codecs and external
# interoperability suites. Override *_COUNT for longer local stress runs.
race:
	go test -race -count=$(RACE_COUNT) -skip '$(EXTERNAL_INTEROP_TEST_PATTERN)' $(CONCURRENCY_TEST_PACKAGES)

leak:
	go test -count=$(LEAK_COUNT) -timeout=$(LEAK_TIMEOUT) -run '$(LIFECYCLE_TEST_PATTERN)' $(LIFECYCLE_TEST_PACKAGES)

# Opt-in, bounded client-side DICOMweb fuzz smoke. Targets run separately
# because the Go fuzz driver accepts one -fuzz expression per invocation.
# Hard input/parser limits live in each target; GOMEMLIMIT is an additional
# soft process safeguard for local and CI smoke runs.
fuzz-smoke:
	@set -eu; \
	cache_dir="$(FUZZ_SMOKE_GOCACHE)"; \
	remove_cache=false; \
	if [ -z "$$cache_dir" ]; then cache_dir="$$(mktemp -d)"; remove_cache=true; fi; \
	if [ "$$remove_cache" = true ]; then trap 'rm -rf -- "$$cache_dir"' EXIT; fi; \
	mkdir -p -- "$$cache_dir"; \
	available_targets="$$(GOCACHE="$$cache_dir" GOMEMLIMIT="$(FUZZ_SMOKE_MEMORY)" go test ./net/dicomweb -list '^Fuzz')"; \
	for target in $(DICOMWEB_FUZZ_TARGETS); do \
		if ! printf '%s\n' "$$available_targets" | grep -Fqx -- "$$target"; then echo "fuzz-smoke: missing target $$target" >&2; exit 1; fi; \
		echo "fuzz-smoke: package=./net/dicomweb target=$$target fuzztime=$(FUZZ_SMOKE_TIME) timeout=$(FUZZ_SMOKE_TIMEOUT) parallel=$(FUZZ_SMOKE_PARALLEL) memory=$(FUZZ_SMOKE_MEMORY) isolated_cache=true"; \
		GOCACHE="$$cache_dir" GOMEMLIMIT="$(FUZZ_SMOKE_MEMORY)" go test ./net/dicomweb -run '^$$' -fuzz="^$${target}$$" -fuzztime="$(FUZZ_SMOKE_TIME)" -timeout="$(FUZZ_SMOKE_TIMEOUT)" -parallel="$(FUZZ_SMOKE_PARALLEL)"; \
	done

# Opt-in independent Part 10 matrix. The selected interpreter must come from
# the pinned environment in scripts/requirements-pydicom-interop.txt.
pydicom-interop:
	@"$(PYDICOM_INTEROP_PYTHON)" -c "import pydicom, numpy, sys; sys.exit(0 if (pydicom.__version__, numpy.__version__) == ('3.0.2', '2.1.3') else 'required: pydicom 3.0.2 and NumPy 2.1.3')"
	DICOM_GO_PYTHON="$(PYDICOM_INTEROP_PYTHON)" DICOM_GO_PYDICOM_PART10=1 go test ./object -run 'PydicomPart10' -count=1 -timeout=$(PYDICOM_INTEROP_TIMEOUT) -v
	DICOM_GO_PYTHON="$(PYDICOM_INTEROP_PYTHON)" DICOM_GO_PYDICOM_CHARSET=1 go test ./object -run '^TestPydicomISO2022Interop$$' -count=1 -timeout=$(PYDICOM_INTEROP_TIMEOUT) -v
	DICOM_GO_PYTHON="$(PYDICOM_INTEROP_PYTHON)" DICOM_GO_PYDICOM_RLE=1 go test ./pixeldata -run '^TestPydicomRLETranscodeInterop$$' -count=1 -timeout=$(PYDICOM_INTEROP_TIMEOUT) -v
	DICOM_GO_PYTHON="$(PYDICOM_INTEROP_PYTHON)" DICOM_GO_PYDICOM_DICOMDIR=1 go test ./dicomdir -run '^TestDICOMDIRPydicomInterop$$' -count=1 -timeout=$(PYDICOM_INTEROP_TIMEOUT) -v

# Informational large-data benchmarks. Run baseline and candidate on the same
# host with identical variables, then compare through benchmark-compare.
# This target is intentionally absent from check: wall-clock results are not a
# deterministic CI acceptance threshold.
benchmark-large:
	@go test ./parser -run '^$$' -bench '^Benchmark(ReaderNext|ReadDataSet)Scalable(ManyElements|DeepSequences|NativePixelData|EncapsulatedPixelData)$$' -benchmem -count=$(BENCH_COUNT) -benchtime=$(BENCH_TIME) -cpu=$(BENCH_CPU) -timeout=$(BENCH_TIMEOUT)
	@go test ./object -run '^$$' -bench '^BenchmarkScalablePart10(Read|Write|RoundTrip|DeferredRead)$$' -benchmem -count=$(BENCH_COUNT) -benchtime=$(BENCH_TIME) -cpu=$(BENCH_CPU) -timeout=$(BENCH_TIMEOUT)

benchmark-compare:
	@if [ -z "$(BASE_BENCH)" ] || [ -z "$(CANDIDATE_BENCH)" ]; then echo "BASE_BENCH and CANDIDATE_BENCH are required" >&2; exit 2; fi
	@BENCHSTAT="$(BENCHSTAT)" bash scripts/benchstat_compare.sh "$(BASE_BENCH)" "$(CANDIDATE_BENCH)"

benchstat-install:
	@if [ -z "$(BENCHSTAT_INSTALL_DIR)" ]; then echo "BENCHSTAT_INSTALL_DIR is required" >&2; exit 2; fi
	@mkdir -p -- "$(BENCHSTAT_INSTALL_DIR)"
	GOBIN="$(BENCHSTAT_INSTALL_DIR)" go install golang.org/x/perf/cmd/benchstat@$(BENCHSTAT_VERSION)

codec-policy-check:
	@bash scripts/codec_policy_check.sh

codec-optional-check:
	@cd examples/codec-adapters/jpeg2000 && CGO_ENABLED=0 go test ./...
	@cd examples/codec-adapters/jpegls && CGO_ENABLED=0 go test ./...
	@cd examples/codec-adapters/jpegxl && CGO_ENABLED=0 go test ./...

codec-jpeg2000-profile-check:
	cd examples/codec-adapters/jpeg2000 && CGO_ENABLED=0 go test ./...
	cd examples/codec-adapters/jpeg2000 && CGO_ENABLED=0 go test -run '^$$' -bench BenchmarkDecodeJPEG2000Profile -benchmem -count=1 -benchtime=1x ./...

codec-jpeg2000-openjpeg-check:
	cd examples/codec-adapters/jpeg2000 && CGO_ENABLED=0 go test -tags jpeg2000_openjpeg ./...

codec-jpegls-charls-check:
	cd examples/codec-adapters/jpegls && CGO_ENABLED=0 go test -tags jpegls_charls ./...

codec-jpegxl-djxl-check:
	cd examples/codec-adapters/jpegxl && CGO_ENABLED=0 go test -tags jpegxl_djxl ./...

# Opt-in production JPEG XL helper. Requires libjxl headers/libs via pkg-config.
codec-jpegxl-helper:
	cd examples/codec-adapters/jpegxl && go test -count=1 -run 'TestProductionHelper|TestHelper|TestHoros' ./...

codecfull-check:
	cd examples/codecfull && CGO_ENABLED=0 go test -tags codecfull ./...

codec-conformance-check:
	go test ./pixeldata/codecfixture

codec-manifest-check:
	go test ./pixeldata/codecprofile ./cmd/dicom-codec-manifest
	go run ./cmd/dicom-codec-manifest -require-ready -validate-only

codec-fixture-bench:
	go test ./pixeldata/codecfixture -run '^$$' -bench BenchmarkDecodeSyntheticCases -benchmem -count=1 -benchtime=1x

jpegxl-inventory:
	go run ./cmd/jpegxl-inventory -dir "$(JPEGXL_FIXTURE_DIR)" -out "$(JPEGXL_MANIFEST)"

jpegxl-inventory-verify:
	go run ./cmd/jpegxl-inventory -dir "$(JPEGXL_FIXTURE_DIR)" -out "$(JPEGXL_MANIFEST)" -verify

# Opt-in JPEG XL per-frame cost campaign. Missing required runtimes fail closed.
# Never overwrite the historical 2026-09-09 campaign. Pass OUT=<new report.json>
# or omit OUT to write a new dated directory. Existing destinations are rejected.
codec-cost-measure:
	@out="$(OUT)"; \
	if [ -z "$$out" ]; then out="docs/benchmarks/$$(date +%Y-%m-%d)-codec-frame-cost/raw/report.json"; fi; \
	case "$$out" in \
	  *2026-09-09-codec-frame-cost*) echo "codec-cost-measure: refusing historical campaign path $$out; pass OUT=<new-path>"; exit 1;; \
	esac; \
	if [ -e "$$out" ]; then echo "codec-cost-measure: refusing to overwrite existing $$out; pass OUT=<new-path>"; exit 1; fi; \
	go run ./examples/codec-cost/cmd/measure -compile-helper -iterations 8 -warmup 1 -require jpegxl-cli -out "$$out"

check: fmt-check vet test build docs-check codec-policy-check codec-optional-check codec-conformance-check codec-manifest-check
