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

for manifest in "$root/go.mod" "$root/go.sum"; do
	[ -f "$manifest" ] || continue
	module_paths="$(manifest_module_paths "$manifest")" || fail "cannot read manifest: ${manifest#$root/}"
	for pattern in "${base_forbidden[@]}"; do
		if grep -Fi "$pattern" <<< "$module_paths" >/dev/null; then
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
# Recurse with Bash 3.2-compatible globs. Do not resolve `find` through PATH:
# Windows Git Bash may select the unrelated native FIND.EXE. Include hidden
# directories and fail on directory symlinks rather than following cycles or
# silently omitting part of the policy surface.
shopt -s nullglob dotglob
check_codec_manifest() {
	local manifest="$1"
	local pattern module_paths
	# Read errors must fail, and grep must consume the full input: grep -q in a
	# pipefail pipeline can turn an early match into an ignored SIGPIPE failure.
	module_paths="$(manifest_module_paths "$manifest")" || fail "cannot read manifest: ${manifest#$root/}"
	for pattern in "${copyleft_forbidden[@]}"; do
		if grep -Fi "$pattern" <<< "$module_paths" >/dev/null; then
			fail "optional manifest ${manifest#$root/} contains forbidden/copyleft pattern '$pattern'"
		fi
	done
}

scan_codec_manifests() {
	local directory="$1"
	local entry
	[ -r "$directory" ] && [ -x "$directory" ] || fail "cannot inspect directory: ${directory#$root/}"
	for entry in "$directory"/*; do
		if [ -d "$entry" ]; then
			[ ! -L "$entry" ] || fail "symlink directory in codec policy tree: ${entry#$root/}"
			scan_codec_manifests "$entry"
		else
			case "${entry##*/}" in
				go.mod|go.sum) check_codec_manifest "$entry" ;;
			esac
		fi
	done
}

scan_codec_manifests "$root/examples/codec-adapters"
scan_codec_manifests "$root/examples/codecfull"

echo "codec-policy-check: OK"
