#!/usr/bin/env bash
set -euo pipefail

# Opt-in Orthanc interoperability matrix runner.
#
# Without DICOMGO_INTEGRATION=1 this script exits successfully after explaining
# how to enable external-peer checks. With DICOMGO_INTEGRATION=1 it requires an
# Orthanc-compatible peer at ORTHANC_HOST:ORTHANC_PORT.

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [[ "${DICOMGO_INTEGRATION:-}" != "1" ]]; then
  echo "Skipping external interop matrix. Set DICOMGO_INTEGRATION=1 to enable." >&2
  exit 0
fi

export ORTHANC_HOST=${ORTHANC_HOST:-127.0.0.1}
export ORTHANC_PORT=${ORTHANC_PORT:-4242}
export CALLING_AET=${CALLING_AET:-DICOMGO}
export CALLED_AET=${CALLED_AET:-ORTHANC}
export MOVE_DEST_AET=${MOVE_DEST_AET:-DICOMSTORE}

cd "${ROOT_DIR}"

echo "Running C-ECHO interop against ${ORTHANC_HOST}:${ORTHANC_PORT}..."
go test ./net/dimse -run '^TestInteropOrthancCEcho$' -count=1

if [[ -n "${STUDY_UID:-}" ]]; then
  echo "Running Study Root C-MOVE interop for STUDY_UID=${STUDY_UID}..."
  go test ./net/dimse -run '^TestInteropOrthancCMoveStudy$' -count=1
else
  echo "Skipping C-MOVE interop because STUDY_UID is not set." >&2
fi
