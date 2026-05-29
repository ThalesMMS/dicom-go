.PHONY: fmt fmt-check vet test build codec-policy-check codec-optional-check codec-jpeg2000-profile-check codec-jpeg2000-openjpeg-check codec-jpegls-charls-check codec-jpegxl-djxl-check codecfull-check codec-conformance-check codec-manifest-check codec-fixture-bench jpegxl-inventory jpegxl-inventory-verify check

JPEGXL_FIXTURE_DIR ?= ../JPEGXL-Fixture
JPEGXL_MANIFEST ?= pixeldata/codecfixture/testdata/codecs/jpegxl_manifest.json

fmt:
	gofmt -w .

fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "Files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	go vet ./...

test:
	go test ./...

build:
	go build ./...

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

check: fmt vet test build codec-policy-check codec-conformance-check codec-manifest-check
