// Package parser decodes DICOM element streams into tokens and in-memory data
// sets.
//
// Implicit VR handling depends on the dictionary.DataDictionary interface
// rather than dictionary/std directly. The parser resolves implicit-VR element
// headers in three steps: it first applies hard-coded transfer-syntax safety
// checks for Pixel Data and Overlay Data, then consults dictionary.LookupVR,
// and finally falls back to UN when no entry is available.
//
// The UN fallback is intentional. Defined-length UN values are preserved as raw
// bytes instead of guessed. Undefined-length SQ values and encapsulated Pixel
// Data are parsed through their structured item/delimiter encoding. For
// Undefined-length UN values use that same sequence grammar while preserving VR
// UN, as required for unknown private sequences. Their nested Value Field is
// decoded with a scoped Implicit VR Little Endian override regardless of the
// enclosing transfer syntax. Private dictionary support is out of scope here,
// so use dictionary.Chain to compose a private overlay with std.Dictionary when
// the private VRs are known.
//
// Implementation notes:
//   - dicom-go expands repeating tags during dictionary generation rather than
//     keeping repeating-tag metadata and performing runtime range lookup.
//   - dicom-go does not expose VirtualVr. Context-dependent VRs are resolved to
//     concrete values during generation, currently xs->US and ox/px/lt->OW.
//   - dicom-go keeps a hard-coded Overlay Data check for all even 60xx groups
//     as a parser safety net, complementing the generated standard dictionary.
package parser
