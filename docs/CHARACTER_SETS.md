# Specific Character Set support

`dicom-go` decodes text VRs through the dataset `SpecificCharacterSet`
(`(0008,0005)`) when a VR uses the DICOM specific character set rules. The
default repertoire is `ISO_IR 6`. Replacement character sets use direct
codecs; multi-valued `ISO 2022` declarations use the DICOM G0/G1 state machine
described in PS3.5 Section 6.1.2.5.

## Supported terms

| DICOM term | Codec path | Decode | Encode | Notes |
|---|---|---:|---:|---|
| empty / absent | built-in default | yes | yes | ASCII / ISO 646 default repertoire. |
| `ISO_IR 6` | built-in default | yes | yes | Also accepts `ISO 2022 IR 6`. |
| `ISO_IR 100` | built-in Latin-1 | yes | yes | Also accepts `ISO 2022 IR 100`. |
| `ISO_IR 101` | `x/text` `iso-8859-2` | yes | yes | Also accepts `ISO 2022 IR 101`. |
| `ISO_IR 109` | `x/text` `iso-8859-3` | yes | yes | Also accepts `ISO 2022 IR 109`. |
| `ISO_IR 110` | `x/text` `iso-8859-4` | yes | yes | Also accepts `ISO 2022 IR 110`. |
| `ISO_IR 126` | `x/text` `iso-ir-126` | yes | yes | Also accepts `ISO 2022 IR 126`. |
| `ISO_IR 127` | `x/text` `iso-ir-127` | yes | yes | Also accepts `ISO 2022 IR 127`. |
| `ISO_IR 138` | `x/text` `iso-ir-138` | yes | yes | Also accepts `ISO 2022 IR 138`. |
| `ISO_IR 144` | `x/text` `iso-ir-144` | yes | yes | Also accepts `ISO 2022 IR 144`. |
| `ISO_IR 148` | `x/text` `iso-ir-148` | yes | yes | Also accepts `ISO 2022 IR 148`. |
| `ISO_IR 166` | `x/text` `iso-8859-11` | yes | yes | Also accepts `ISO 2022 IR 166`. |
| `ISO_IR 13` | built-in JIS X 0201 + `x/text` tables | yes | yes | Restricted to JIS Roman in G0 and half-width Katakana in G1; `ISO 2022 IR 13` participates in G1 designation. |
| `ISO 2022 IR 87` | DICOM ISO 2022 + `x/text` EUC-JP tables | yes | yes | JIS X 0208 in G0. |
| `ISO 2022 IR 159` | DICOM ISO 2022 + `x/text` EUC-JP tables | yes | yes | JIS X 0212 supplementary characters in G0. |
| `ISO 2022 IR 149` | DICOM ISO 2022 + `x/text` EUC-KR tables | yes | yes | KS X 1001 in G1. |
| `ISO 2022 IR 58` | DICOM ISO 2022 + `x/text` GB 2312 tables | yes | yes | GB 2312 in G1. |
| `ISO_IR 192` | built-in UTF-8 | yes | yes | UTF-8. |
| `GB18030` | `x/text` `gb18030` | yes | yes | Chinese national standard. |
| `GBK` | `x/text` `gbk` | yes | yes | Chinese GBK extension. |

Term matching is case-insensitive and accepts either underscores or spaces, for
example `ISO_IR 100`, `ISO IR 100`, and `iso_ir_100`.

Declarations are validated before codec construction. Replacement repertoires
such as UTF-8, GBK, GB18030, and the `ISO_IR` terms must be single-valued. Code
Extension declarations must be multi-valued, use only `ISO 2022` terms, place
an empty value or an allowed single-byte repertoire in Value 1, and place
multi-byte repertoires only in Values 2 through n. Empty trailing values,
mixed replacement/extension terms, invalid initial repertoires, and duplicate
repertoires return `ErrInvalidCharsetDeclaration` with the offending value.

## Person Name handling

For PN values, `^` and `=` are interpreted only at encoded-character
boundaries. They restore the initial repertoire; an extension used after either
delimiter must be designated again. This avoids treating a delimiter-valued
byte inside a two-byte Japanese character as syntax. The same reset model is
used for value separators, line/page boundaries, and permitted C0 controls.
Code Extension is rejected in the first (alphabetic) PN component group as
required by PS3.5; ideographic and phonetic groups may use the declared
extensions.

The decoder accepts only declared DICOM escape sequences, fixed G0/GL and
G1/GR invocation, and complete characters in their registered byte areas.
G2/G3, locking or single shifts, C1 controls, unknown escapes, undeclared
repertoires, truncated characters, and a missing G0 restoration fail with an
`ErrInvalidCodeExtension`. `CodeExtensionError` reports the byte offset and a
bounded hexadecimal sequence without copying the surrounding value.

The encoder chooses the first declared repertoire that represents a character,
reuses the current designation where possible, emits the required escape after
each reset, and restores the initial G0 repertoire before delimiters, controls,
and the end of the value.

Conformance tests use the byte-exact Japanese, Korean, and Chinese examples from
DICOM PS3.5 Annexes H, I, and K. JIS X 0212 and JIS X 0201 expectations are also
cross-checked against independent pydicom fixtures rather than generated from
the production codec.

## Limitations

- Character set detection is not automatic. If `(0008,0005)` is absent, the
  default repertoire is used.
- ISO 2022 designation is intentionally limited to the DICOM-defined terms in
  the table. Generic ISO 2022 profiles and escape sequences outside PS3.5 are
  rejected.
- Unsupported terms return `ErrUnsupportedCharset`. Callers may opt into
  `object.TextOptions.AllowUnsupportedCharsetFallback` when a deployment needs a
  controlled fallback.
