#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: benchstat_compare.sh BASE.txt CANDIDATE.txt" >&2
  exit 2
fi

baseline=$1
candidate=$2
benchstat=${BENCHSTAT:-benchstat}

for result in "$baseline" "$candidate"; do
  if [[ ! -f "$result" ]]; then
    echo "benchmark result not found: $result" >&2
    exit 2
  fi
done

temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT

benchmark_samples() {
  awk '
    /^Benchmark/ {
      name=$1
      sub(/-[0-9]+$/, "", name)
      samples[name]++
    }
    END {
      for (name in samples) print name, samples[name]
    }
  ' "$1" | LC_ALL=C sort
}

benchmark_samples "$baseline" > "$temporary/baseline.samples"
benchmark_samples "$candidate" > "$temporary/candidate.samples"

if [[ ! -s "$temporary/baseline.samples" || ! -s "$temporary/candidate.samples" ]]; then
  echo "both inputs must contain Go benchmark results" >&2
  exit 2
fi

if ! diff -u "$temporary/baseline.samples" "$temporary/candidate.samples"; then
  echo "refusing to compare different sub-benchmark names or sample counts" >&2
  exit 2
fi

"$benchstat" "$baseline" "$candidate"
