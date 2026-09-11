// Package curated supplies an optional, provenance-bearing private dictionary.
// Importing it never changes the standard dictionary or de-identification rules.
package curated

import (
	_ "embed"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
)

//go:generate go run ../../cmd/privatecataloggen -source catalog.json -out catalog_gen.go

// Origin identifies the reviewed upstream definition, not a manufacturer's
// validation of every scanner version or permission to retain private data.
type Origin struct {
	Project, Version, Commit   string
	SourceURL, SourceSHA256    string
	SourceLine                 int
	DefinitionSHA256           string
	License, LicenseURL        string
	Restrictions, Verification string
}

// Record is a detached definition and its audit origin. Creator/group/offset
// identify a definition independently of the block used in an actual dataset.
type Record struct {
	Definition dictionary.PrivateEntry
	Origin     Origin
}

type key struct {
	creator string
	group   uint16
	offset  uint8
}

// Catalog is immutable and implements dictionary.PrivateDataDictionary.
type Catalog struct {
	definitions *dictionary.PrivateCatalog
	records     []Record
	byKey       map[key]int
}

var _ dictionary.PrivateDataDictionary = (*Catalog)(nil)

// New constructs the explicitly selected catalog. Compose it with
// dictionary.Chain{catalog, std.Dictionary}; put local overlays first.
func New() (*Catalog, error) {
	c := &Catalog{records: append([]Record(nil), records...), byKey: make(map[key]int, len(records))}
	entries := make([]dictionary.PrivateEntry, len(records))
	for i, r := range c.records {
		entries[i] = r.Definition
		c.byKey[key{r.Definition.Creator, r.Definition.Group, r.Definition.Offset}] = i
	}
	var err error
	c.definitions, err = dictionary.NewPrivateCatalog(entries)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (*Catalog) ByTag(core.Tag) (dictionary.Entry, bool)   { return dictionary.Entry{}, false }
func (*Catalog) ByKeyword(string) (dictionary.Entry, bool) { return dictionary.Entry{}, false }
func (c *Catalog) ByPrivate(creator string, group uint16, offset uint8) (dictionary.Entry, bool) {
	if c == nil {
		return dictionary.Entry{}, false
	}
	return c.definitions.ByPrivate(creator, group, offset)
}

// Lookup returns this catalog's definition and origin. When a preceding local
// overlay wins in a Chain, its definition must be audited at that provider.
func (c *Catalog) Lookup(creator string, group uint16, offset uint8) (Record, bool) {
	if c == nil {
		return Record{}, false
	}
	canonical, err := dictionary.PrivateCreatorID(creator)
	if err != nil {
		return Record{}, false
	}
	i, ok := c.byKey[key{canonical, group, offset}]
	if !ok {
		return Record{}, false
	}
	return c.records[i], true
}

func (c *Catalog) Entries() []Record {
	if c == nil {
		return nil
	}
	return append([]Record(nil), c.records...)
}

//go:embed LICENSE.DCMTK
var licenseNotice string

// LicenseNotice returns the attribution and terms for redistributed entries.
func LicenseNotice() string { return licenseNotice }
