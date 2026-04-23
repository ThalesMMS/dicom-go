// Package parser decodes DICOM element streams into tokens and in-memory data
// sets.
//
// Implicit VR handling depends on the dictionary.DataDictionary interface
// rather than dictionary/std directly. The parser resolves implicit-VR element
// headers in three steps: it first applies hard-coded transfer-syntax safety
// checks for Pixel Data and Overlay Data, then consults dictionary.LookupVR,
// and finally falls back to UN when no entry is available.
//
// The UN fallback is intentional. For defined-length values it preserves the
// payload as raw bytes instead of guessing a VR. For undefined-length values it
// returns ErrUnsupportedUndefinedLength because the parser cannot safely infer
// whether the payload is a sequence or another undefined-length container
// without dictionary support. This is especially relevant for private tags:
// private dictionary support is out of scope here, so odd-group private tags
// usually resolve to UN unless callers inject a custom dictionary.
//
package parser
