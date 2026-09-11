"""Decode exported corpus inputs with Pillow/libjpeg-turbo; write candidates only."""
import argparse
import hashlib
import json
from pathlib import Path

import PIL
from PIL import Image, features, _imaging


def sha(data):
    return hashlib.sha256(data).hexdigest()


parser = argparse.ArgumentParser()
parser.add_argument("--directory", type=Path, required=True)
args = parser.parse_args()
record = {
    "backend": "libjpeg-turbo",
    "version": features.version_feature("libjpeg_turbo"),
    "wrapper": "Pillow " + PIL.__version__,
    "binarySha256": sha(Path(_imaging.__file__).read_bytes()),
    "generator": "generate-jpeg-assembly-inputs.go + generate-jpeg-assembly-oracles.py v1",
    "source": "existing project-authored synthetic JPEG Baseline/SOF1 two-pixel corpus; no PHI",
    "license": "project fixtures MIT; libjpeg-turbo BSD-3-Clause/IJG; Pillow HPND",
    "fixtures": [],
}
if not record["version"]:
    raise RuntimeError("libjpeg-turbo backend required")
for name in ["jpeg-baseline-small", "jpeg-extended-small"]:
    path = args.directory / (name + ".jpg")
    with Image.open(path) as image:
        image.load()
        if image.mode != "L" or image.size != (2, 1):
            raise RuntimeError("fixture metadata drift")
        data = image.tobytes()
    raw = path.with_suffix(".raw")
    raw.write_bytes(data)
    record["fixtures"].append({"name": name, "path": path.name, "sha256": sha(path.read_bytes()), "referencePath": raw.name, "referenceSha256": sha(data)})
(args.directory / "generation.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
print(json.dumps(record, indent=2))
