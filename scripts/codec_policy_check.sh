#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
policy_doc="$root/docs/CODEC_DEPENDENCY_POLICY.md"
jpegxl_doc="$root/docs/JPEGXL_DECODER_STRATEGY.md"

fail() {
	echo "codec-policy-check: $*" >&2
	exit 1
}

require_file() {
	[ -f "$1" ] || fail "missing required file: ${1#$root/}"
}

require_grep() {
	local needle="$1"
	local file="$2"
	grep -Fq "$needle" "$file" || fail "${file#$root/} must mention '$needle'"
}

manifest_module_paths() {
	local file="$1"
	case "$file" in
		*.mod)
			awk '
				$1 == "module" { print $2 }
				$1 == "require" && $2 != "(" { print $2 }
				$1 == "replace" {
					print $2
					if ($3 == "=>") {
						print $4
					}
				}
				$1 ~ /^[[:alnum:]_.\/-]+$/ && $2 ~ /^v/ { print $1 }
				$1 ~ /^[[:alnum:]_.\/-]+$/ && $2 == "=>" {
					print $1
					print $3
				}
			' "$file"
			;;
		*.sum)
			awk '{ print $1 }' "$file"
			;;
	esac
}

require_file "$policy_doc"
require_file "$jpegxl_doc"
require_file "$root/go.mod"
require_file "$root/examples/codec-adapters/jpeg2000/go.mod"
require_file "$root/examples/codec-adapters/jpegls/go.mod"
require_file "$root/examples/codec-adapters/jpegxl/go.mod"
require_file "$root/examples/codecfull/go.mod"

for needle in \
	"default" \
	"jpeg2000" \
	"jpegls_charls" \
	"jpegxl_djxl" \
	"codecfull" \
	"MIT" \
	"BSD" \
	"Apache" \
	"LGPL, GPL, AGPL" \
	"go test ./..." \
	"make check"; do
	require_grep "$needle" "$policy_doc"
done

for needle in \
	"libjxl" \
	"github.com/gen2brain/jpegxl" \
	"go 1.25.0" \
	"DependencyUnavailableJPEGXL" \
	"must not enter the base module"; do
	require_grep "$needle" "$jpegxl_doc"
done

base_forbidden=(
	"github.com/ThalesMMS/dicom-go/examples/codec-adapters/"
	"github.com/mrjoshuak/go-jpeg2000"
	"charls"
	"openjpeg"
	"grok"
	"libjxl"
	"github.com/gen2brain/jpegxl"
)

for pattern in "${base_forbidden[@]}"; do
	for manifest in "$root/go.mod" "$root/go.sum"; do
		[ -f "$manifest" ] || continue
		if manifest_module_paths "$manifest" | grep -Fqi "$pattern"; then
			fail "base module manifest must not depend on optional/native codec pattern '$pattern'"
		fi
	done
done

for module in jpeg2000 jpegls jpegxl; do
	modfile="$root/examples/codec-adapters/$module/go.mod"
	require_grep "module github.com/ThalesMMS/dicom-go/examples/codec-adapters/$module" "$modfile"
	require_grep "replace github.com/ThalesMMS/dicom-go => ../../.." "$modfile"
done

require_grep "module github.com/ThalesMMS/dicom-go/examples/codecfull" "$root/examples/codecfull/go.mod"
require_grep "replace github.com/ThalesMMS/dicom-go => ../.." "$root/examples/codecfull/go.mod"

copyleft_forbidden=( "agpl" "lgpl" "gpl" "grok" )
while IFS= read -r manifest; do
	for pattern in "${copyleft_forbidden[@]}"; do
		if manifest_module_paths "$manifest" | grep -Fqi "$pattern"; then
			fail "optional manifest ${manifest#$root/} contains forbidden/copyleft pattern '$pattern'"
		fi
	done
done < <(find "$root/examples/codec-adapters" "$root/examples/codecfull" -type f \( -name go.mod -o -name go.sum \))

echo "codec-policy-check: OK"
