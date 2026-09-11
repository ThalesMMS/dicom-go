# Third-party notices

## Horos — VR Muscles-Bones color lookup table

`render/resources/vr_muscles_bones_rgb.hex` contains the 256 unmodified 8-bit
RGB triplets from `Horos/Resources/CLUTs/VR Muscles-Bones.plist` in the Horos
project at commit `2a130506f2ae651fcd52becbd5469c107bbbf795`.

- Upstream: <https://github.com/horosproject/horos>
- Source file: `Horos/Resources/CLUTs/VR Muscles-Bones.plist`
- Copyright: Horos Project contributors and the OsiriX Team, as identified by
  the upstream project notice
- License: GNU Lesser General Public License, version 3 (LGPL-3.0)
- Distributed `.hex` file SHA-256:
  `f837f2833467b02d93fdc7a2998ce1ba996fe376a6505a06da91b0aa5953bcf9`
- Decoded interleaved RGB SHA-256:
  `6080ca9c271bb466d1994a6d5d5fd5cdb249150b0c99a0c313d871dbabcdc34e`

The numerical table is kept separate from the independently implemented Go
sampling, sRGB conversion, transfer-function, and rendering logic. The upstream
license text is available at <https://www.gnu.org/licenses/lgpl-3.0.txt> and in
the Horos repository as `COPYING.LESSER`.

## DCMTK — selected private dictionary definitions

`dictionary/curated/catalog.json` and its generated Go records contain 21
selected definitions from DCMTK 3.6.9 `dcmdata/data/private.dic`, commit
`ac002900cab167509881e5b837cdef5dcb07cd37`. The dictionary bears Copyright (C)
1994–2020 OFFIS e.V.; the release's main copyright notice covers 1994–2024.
The applicable BSD-3-Clause conditions and disclaimer are preserved in
[`dictionary/curated/LICENSE.DCMTK`](dictionary/curated/LICENSE.DCMTK) and exposed
by `curated.LicenseNotice()` for binary distribution notices. Retain that notice
when redistributing these definitions. No OFFIS endorsement is implied.

Every entry identifies its exact source row, line, commit, file hash and license
URL. See [catalog provenance and regeneration](docs/CURATED_PRIVATE_CATALOG.md).
