#!/usr/bin/env bash
set -euo pipefail

# Interop: C-MOVE (Study Root) against Orthanc
#
# This is a reproducible *manual* interop script intended for local runs.
# It assumes you have Orthanc running (see docs/INTEROP_ORTHANC.md).
#
# What it does:
#   1. Builds the dicom-go tools.
#   2. Issues a C-MOVE request using the demo retrieve SCU.
#
# Notes:
#   - C-MOVE requires a Move Destination AE that runs a C-STORE SCP.
#     Orthanc will open a separate association to that destination to send instances.
#

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

ORTHANC_HOST=${ORTHANC_HOST:-127.0.0.1}
ORTHANC_PORT=${ORTHANC_PORT:-4242}
CALLING_AET=${CALLING_AET:-DICOMGO}
CALLED_AET=${CALLED_AET:-ORTHANC}
MOVE_DEST_AET=${MOVE_DEST_AET:-DICOMSTORE}

# Example query: move a single StudyInstanceUID
STUDY_UID=${STUDY_UID:-}

if [[ -z "${STUDY_UID}" ]]; then
  echo "STUDY_UID is required (e.g. export STUDY_UID=1.2.3...)." >&2
  exit 1
fi

cd "${ROOT_DIR}"

go build ./...

echo "Running C-MOVE interop against Orthanc ${ORTHANC_HOST}:${ORTHANC_PORT} (CalledAET=${CALLED_AET})..."

go run ./cmd/dicom-go-retrieve \
  -remote "${ORTHANC_HOST}:${ORTHANC_PORT}" \
  -calling-aet "${CALLING_AET}" \
  -called-aet "${CALLED_AET}" \
  -move-destination "${MOVE_DEST_AET}" \
  -level STUDY \
  -study-uid "${STUDY_UID}"
