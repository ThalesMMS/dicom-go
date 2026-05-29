// Package codecfixture builds synthetic DICOM pixel-data conformance cases.
//
// The package is intentionally public so applications that consume dicom-go,
// including Twin-Viewer, can prove that the same generated fixture shape loads
// through their integration paths. The default corpus is generated in memory
// from synthetic pixel values and carries no PHI.
package codecfixture
