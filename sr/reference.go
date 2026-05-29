package sr

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"math"
	"strconv"
	"strings"
)

// ContentItemIdentifier is the normative one-based path of an SR Content Item.
// The root Content Item is {1}; each following component is the ordinal of an
// item in its parent's Content Sequence, including by-reference relationship
// slots.
type ContentItemIdentifier []uint32

// Clone returns a caller-owned copy of the identifier.
func (identifier ContentItemIdentifier) Clone() ContentItemIdentifier {
	return append(ContentItemIdentifier(nil), identifier...)
}

// String returns a value-free, stable path representation suitable for
// diagnostics.
func (identifier ContentItemIdentifier) String() string {
	if len(identifier) == 0 {
		return ""
	}
	parts := make([]string, len(identifier))
	for index, component := range identifier {
		parts[index] = strconv.FormatUint(uint64(component), 10)
	}
	return strings.Join(parts, ".")
}

func (identifier ContentItemIdentifier) valid(maxComponents int) bool {
	if len(identifier) == 0 || identifier[0] != 1 || len(identifier) > maxComponents {
		return false
	}
	for _, component := range identifier {
		if component == 0 {
			return false
		}
	}
	return true
}

func (identifier ContentItemIdentifier) equal(other ContentItemIdentifier) bool {
	if len(identifier) != len(other) {
		return false
	}
	for index := range identifier {
		if identifier[index] != other[index] {
			return false
		}
	}
	return true
}

func (identifier ContentItemIdentifier) ancestorOf(other ContentItemIdentifier) bool {
	if len(identifier) >= len(other) {
		return false
	}
	for index := range identifier {
		if identifier[index] != other[index] {
			return false
		}
	}
	return true
}

func identifierKey(identifier ContentItemIdentifier) string {
	raw := make([]byte, len(identifier)*4)
	for index, component := range identifier {
		binary.BigEndian.PutUint32(raw[index*4:], component)
	}
	return string(raw)
}

// ValidationMode controls whether invalid optional validation input rejects an
// operation or is retained with diagnostics.
type ValidationMode uint8

const (
	ValidationModeStrict ValidationMode = iota + 1
	ValidationModeWarn
)

// DiagnosticSeverity is intentionally small and value-free.
type DiagnosticSeverity string

const (
	DiagnosticError   DiagnosticSeverity = "error"
	DiagnosticWarning DiagnosticSeverity = "warning"
)

// DiagnosticFinding describes a structural SR violation without echoing text,
// person names, UIDs, code meanings, or other potentially identifying values.
type DiagnosticFinding struct {
	Path     ContentItemIdentifier
	Target   ContentItemIdentifier
	RuleID   string
	Code     string
	Severity DiagnosticSeverity
	Message  string
}

// ValidationReport is shared by reference and template validation.
type ValidationReport struct {
	Findings  []DiagnosticFinding
	Truncated bool
}

// HasErrors reports whether the report contains an error-severity finding.
func (report ValidationReport) HasErrors() bool {
	for _, finding := range report.Findings {
		if finding.Severity == DiagnosticError {
			return true
		}
	}
	return false
}

func (report ValidationReport) clone() ValidationReport {
	out := ValidationReport{Truncated: report.Truncated, Findings: make([]DiagnosticFinding, len(report.Findings))}
	copy(out.Findings, report.Findings)
	for index := range out.Findings {
		out.Findings[index].Path = out.Findings[index].Path.Clone()
		out.Findings[index].Target = out.Findings[index].Target.Clone()
	}
	return out
}

var (
	// ErrReferenceResolution identifies an invalid SR by-reference graph.
	ErrReferenceResolution = errors.New("dicom/sr: reference resolution failed")
	// ErrStaleReferenceIndex reports navigation attempted after the mutable
	// content tree changed. Call ResolveReferences again explicitly.
	ErrStaleReferenceIndex = errors.New("dicom/sr: stale reference index")
)

const (
	ReferenceCodeInvalidPath       = "sr.reference.invalid_path"
	ReferenceCodeDangling          = "sr.reference.dangling"
	ReferenceCodeSelf              = "sr.reference.self"
	ReferenceCodeAncestor          = "sr.reference.ancestor"
	ReferenceCodeCycle             = "sr.reference.cycle"
	ReferenceCodeForbiddenProfile  = "sr.reference.forbidden_profile"
	ReferenceCodeForbiddenRelation = "sr.reference.forbidden_relationship"
	ReferenceCodeIncompatible      = "sr.reference.incompatible_target"
	ReferenceCodeByValueMacro      = "sr.reference.by_value_macro"
	ReferenceCodeMissingRelation   = "sr.reference.missing_relationship"
	ReferenceCodeResourceLimit     = "sr.reference.resource_limit"
)

// ReferenceError is a typed, PHI-free resolution failure.
type ReferenceError struct {
	Code   string
	Source ContentItemIdentifier
	Slot   ContentItemIdentifier
	Target ContentItemIdentifier
}

func (err *ReferenceError) Error() string {
	if err == nil {
		return ErrReferenceResolution.Error()
	}
	return fmt.Sprintf("%s: %s", ErrReferenceResolution, err.Code)
}

func (err *ReferenceError) Unwrap() error { return ErrReferenceResolution }

// ReferenceOptions bounds reference indexing and selects strict or warn mode.
type ReferenceOptions struct {
	Mode              ValidationMode
	MaxDepth          int
	MaxItems          int
	MaxReferences     int
	MaxPathComponents int
	MaxFindings       int
}

// DefaultReferenceOptions returns bounded strict validation defaults.
func DefaultReferenceOptions() ReferenceOptions {
	return ReferenceOptions{
		Mode:              ValidationModeStrict,
		MaxDepth:          64,
		MaxItems:          100_000,
		MaxReferences:     100_000,
		MaxPathComponents: 65,
		MaxFindings:       1_024,
	}
}

func normalizeReferenceOptions(options ReferenceOptions) ReferenceOptions {
	defaults := DefaultReferenceOptions()
	if options.Mode == 0 {
		options.Mode = defaults.Mode
	}
	if options.MaxDepth == 0 {
		options.MaxDepth = defaults.MaxDepth
	}
	if options.MaxItems == 0 {
		options.MaxItems = defaults.MaxItems
	}
	if options.MaxReferences == 0 {
		options.MaxReferences = defaults.MaxReferences
	}
	if options.MaxPathComponents == 0 {
		options.MaxPathComponents = defaults.MaxPathComponents
	}
	if options.MaxFindings == 0 {
		options.MaxFindings = defaults.MaxFindings
	}
	return options
}

func validateReferenceOptions(options ReferenceOptions) error {
	if options.Mode != ValidationModeStrict && options.Mode != ValidationModeWarn {
		return fmt.Errorf("%w: invalid validation mode", ErrReferenceResolution)
	}
	if options.MaxDepth < 1 || options.MaxItems < 1 || options.MaxReferences < 1 ||
		options.MaxPathComponents < 1 || options.MaxFindings < 1 {
		return fmt.Errorf("%w: invalid resource limits", ErrReferenceResolution)
	}
	return nil
}

// ReferenceEdge preserves the encoded relationship slot separately from its
// enclosing source and referenced target.
type ReferenceEdge struct {
	Source           ContentItemIdentifier
	Slot             ContentItemIdentifier
	Target           ContentItemIdentifier
	RelationshipType string
}

func (edge ReferenceEdge) clone() ReferenceEdge {
	edge.Source = edge.Source.Clone()
	edge.Slot = edge.Slot.Clone()
	edge.Target = edge.Target.Clone()
	return edge
}

// ReferenceIndex is an immutable path graph bound to one mutable Document
// shape. It stores no ContentItem pointers and detects structural changes before
// every query.
type ReferenceIndex struct {
	document    *Document
	fingerprint [sha256.Size]byte
	options     ReferenceOptions
	edges       []ReferenceEdge
	bySource    map[string][]int
	byTarget    map[string][]int
	bySlot      map[string]int
}

type referenceNode struct {
	valueType ValueType
}

type referenceWalkEntry struct {
	item   *ContentItem
	parent ContentItemIdentifier
	path   ContentItemIdentifier
	depth  int
}

type referenceWalkFrame struct {
	items  []ContentItem
	parent ContentItemIdentifier
	next   int
	depth  int
}

type referenceWalker struct {
	frames []referenceWalkFrame
}

func newReferenceWalker(items []ContentItem, parent ContentItemIdentifier, depth int) *referenceWalker {
	walker := &referenceWalker{}
	walker.push(items, parent, depth)
	return walker
}

func (walker *referenceWalker) push(items []ContentItem, parent ContentItemIdentifier, depth int) {
	if len(items) == 0 {
		return
	}
	walker.frames = append(walker.frames, referenceWalkFrame{items: items, parent: parent, depth: depth})
}

func (walker *referenceWalker) next() (referenceWalkEntry, bool) {
	for len(walker.frames) > 0 {
		frame := &walker.frames[len(walker.frames)-1]
		if frame.next >= len(frame.items) {
			walker.frames = walker.frames[:len(walker.frames)-1]
			continue
		}
		index := frame.next
		frame.next++
		path := appendPath(frame.parent, uint32(index+1))
		return referenceWalkEntry{
			item: &frame.items[index], parent: frame.parent,
			path: path, depth: frame.depth,
		}, true
	}
	return referenceWalkEntry{}, false
}

// ResolveReferences indexes and validates every by-reference relationship
// after the complete tree has been assembled. Encoded paths remain unchanged.
func ResolveReferences(document *Document, options ReferenceOptions) (*ReferenceIndex, ValidationReport, error) {
	options = normalizeReferenceOptions(options)
	if err := validateReferenceOptions(options); err != nil {
		return nil, ValidationReport{}, err
	}
	if document == nil {
		return nil, ValidationReport{}, fmt.Errorf("%w: nil document", ErrReferenceResolution)
	}

	index := &ReferenceIndex{
		document: document,
		options:  options,
		bySource: map[string][]int{},
		byTarget: map[string][]int{},
		bySlot:   map[string]int{},
	}
	report := ValidationReport{}
	root := ContentItemIdentifier{1}
	rootKey := identifierKey(root)
	nodes := map[string]referenceNode{rootKey: {valueType: ValueContainer}}
	nodeOrder := []string{rootKey}
	walker := newReferenceWalker(document.Content, root, 1)
	itemCount := 0
	referenceCount := 0
	resourceLimited := false
	invalidSlots := map[string]struct{}{}
	for {
		entry, ok := walker.next()
		if !ok {
			break
		}
		itemCount++
		if itemCount > options.MaxItems || entry.depth > options.MaxDepth {
			addReferenceResourceFinding(&report, options, entry.path)
			resourceLimited = true
			break
		}
		item := entry.item
		if item.Encoding.EmptyReferenceMacroPresent || len(item.ReferencedContentItemIdentifier) > 0 {
			referenceCount++
			if referenceCount > options.MaxReferences {
				addReferenceResourceFinding(&report, options, entry.path)
				resourceLimited = true
				break
			}
			edge := ReferenceEdge{
				Source: entry.parent.Clone(), Slot: entry.path.Clone(),
				Target:           item.ReferencedContentItemIdentifier.Clone(),
				RelationshipType: item.RelationshipType,
			}
			index.edges = append(index.edges, edge)
			if validationErr := validateEncodedReferenceItem(*item, options.MaxPathComponents); validationErr != nil {
				code := ReferenceCodeByValueMacro
				var referenceErr *ReferenceError
				if errors.As(validationErr, &referenceErr) && referenceErr.Code != "" {
					code = referenceErr.Code
				}
				addReferenceFinding(&report, options, code, edge.Slot, edge.Target)
				invalidSlots[identifierKey(edge.Slot)] = struct{}{}
			}
			if _, invalid := invalidSlots[identifierKey(edge.Slot)]; !invalid && !edge.Target.valid(options.MaxPathComponents) {
				addReferenceFinding(&report, options, ReferenceCodeInvalidPath, edge.Slot, edge.Target)
				invalidSlots[identifierKey(edge.Slot)] = struct{}{}
			}
			continue
		}

		pathKey := identifierKey(entry.path)
		nodes[pathKey] = referenceNode{valueType: item.ValueType}
		nodeOrder = append(nodeOrder, pathKey)
		walker.push(item.Children, entry.path, entry.depth+1)
	}
	if resourceLimited {
		return nil, report.clone(), fmt.Errorf("%w: %w", ErrReferenceResolution, ErrResourceLimitExceeded)
	}

	cycleEdges := make([]ReferenceEdge, 0, len(index.edges))
	for edgeIndex := range index.edges {
		edge := index.edges[edgeIndex]
		if _, invalid := invalidSlots[identifierKey(edge.Slot)]; invalid {
			continue
		}
		source := nodes[identifierKey(edge.Source)]
		target, targetOK := nodes[identifierKey(edge.Target)]
		if !targetOK {
			addReferenceFinding(&report, options, ReferenceCodeDangling, edge.Slot, edge.Target)
			continue
		}
		if edge.Target.equal(edge.Source) {
			addReferenceFinding(&report, options, ReferenceCodeSelf, edge.Slot, edge.Target)
			continue
		}
		if edge.Target.ancestorOf(edge.Source) {
			addReferenceFinding(&report, options, ReferenceCodeAncestor, edge.Slot, edge.Target)
			continue
		}
		if profileForbidsByReference(document.SOPClassUID) {
			addReferenceFinding(&report, options, ReferenceCodeForbiddenProfile, edge.Slot, edge.Target)
			continue
		}
		if referenceRelationshipForbidden(document.SOPClassUID, edge.RelationshipType) {
			addReferenceFinding(&report, options, ReferenceCodeForbiddenRelation, edge.Slot, edge.Target)
			continue
		}
		if !referenceRelationshipAllowed(document.SOPClassUID, source.valueType, edge.RelationshipType, target.valueType) {
			addReferenceFinding(&report, options, ReferenceCodeIncompatible, edge.Slot, edge.Target)
			continue
		}
		cycleEdges = append(cycleEdges, edge)
	}
	detectReferenceCycles(cycleEdges, nodes, nodeOrder, &report, options)

	for edgeIndex := range index.edges {
		edge := index.edges[edgeIndex]
		index.bySource[identifierKey(edge.Source)] = append(index.bySource[identifierKey(edge.Source)], edgeIndex)
		index.byTarget[identifierKey(edge.Target)] = append(index.byTarget[identifierKey(edge.Target)], edgeIndex)
		index.bySlot[identifierKey(edge.Slot)] = edgeIndex
	}
	fingerprint, fingerprintErr := fingerprintDocument(document, options)
	if fingerprintErr != nil {
		addReferenceResourceFinding(&report, options, root)
		return nil, report.clone(), fmt.Errorf("%w: %w", ErrReferenceResolution, ErrResourceLimitExceeded)
	} else {
		index.fingerprint = fingerprint
	}
	if options.Mode == ValidationModeStrict && report.HasErrors() {
		finding := report.Findings[0]
		var source ContentItemIdentifier
		if edgeIndex, ok := index.bySlot[identifierKey(finding.Path)]; ok {
			source = index.edges[edgeIndex].Source.Clone()
		}
		return index, report.clone(), &ReferenceError{Code: finding.Code, Source: source, Slot: finding.Path.Clone(), Target: finding.Target.Clone()}
	}
	return index, report.clone(), nil
}

func referenceRelationshipForbidden(sopClassUID, relationship string) bool {
	switch clean(sopClassUID) {
	case ComprehensiveSRStorage, Comprehensive3DSRStorage:
		return relationship == RelationshipContains || relationship == RelationshipHasConceptMod
	case ExtensibleSRStorage:
		return relationship == RelationshipContains
	default:
		return false
	}
}

func addPathFinding(report *ValidationReport, options ReferenceOptions, finding DiagnosticFinding) {
	if len(report.Findings) >= options.MaxFindings {
		report.Truncated = true
		return
	}
	if options.Mode == ValidationModeWarn {
		finding.Severity = DiagnosticWarning
	} else {
		finding.Severity = DiagnosticError
	}
	report.Findings = append(report.Findings, finding)
}

func addReferenceFinding(report *ValidationReport, options ReferenceOptions, code string, slot, target ContentItemIdentifier) {
	addPathFinding(report, options, DiagnosticFinding{
		Path: slot.Clone(), Target: target.Clone(), RuleID: "PS3.3.C.17.3.4",
		Code: code, Severity: DiagnosticError, Message: referenceMessage(code),
	})
}

func addReferenceResourceFinding(report *ValidationReport, options ReferenceOptions, path ContentItemIdentifier) {
	before := len(report.Findings)
	addReferenceFinding(report, options, ReferenceCodeResourceLimit, path, nil)
	if len(report.Findings) > before {
		report.Findings[len(report.Findings)-1].Severity = DiagnosticError
	}
}

func referenceMessage(code string) string {
	switch code {
	case ReferenceCodeInvalidPath:
		return "referenced content item identifier is invalid"
	case ReferenceCodeDangling:
		return "referenced content item does not exist"
	case ReferenceCodeSelf:
		return "content item references itself"
	case ReferenceCodeAncestor:
		return "content item references an ancestor"
	case ReferenceCodeCycle:
		return "content item references form a cycle"
	case ReferenceCodeForbiddenProfile:
		return "SOP class forbids by-reference relationships"
	case ReferenceCodeForbiddenRelation:
		return "relationship type cannot be conveyed by-reference"
	case ReferenceCodeIncompatible:
		return "source and target value types are incompatible"
	case ReferenceCodeByValueMacro:
		return "by-reference item also carries by-value content"
	case ReferenceCodeMissingRelation:
		return "by-reference item has no relationship type"
	default:
		return "reference validation resource limit exceeded"
	}
}

func appendPath(parent ContentItemIdentifier, child uint32) ContentItemIdentifier {
	out := make(ContentItemIdentifier, len(parent)+1)
	copy(out, parent)
	out[len(parent)] = child
	return out
}

func profileForbidsByReference(sopClassUID string) bool {
	switch clean(sopClassUID) {
	case BasicTextSRStorage, EnhancedSRStorage, KeyObjectSelectionDocumentStorage:
		return true
	default:
		return false
	}
}

func referenceRelationshipAllowed(sopClassUID string, source ValueType, relationship string, target ValueType) bool {
	if relationship == "" {
		return false
	}
	if clean(sopClassUID) != ComprehensiveSRStorage && clean(sopClassUID) != Comprehensive3DSRStorage {
		return true
	}
	contains := func(value ValueType, allowed ...ValueType) bool {
		for _, candidate := range allowed {
			if value == candidate {
				return true
			}
		}
		return false
	}
	commonObservationTargets := []ValueType{ValueText, ValueCode, ValueNum, ValueDateTime, ValueDate, ValueTime, ValueUIDRef, ValuePName, ValueComposite}
	commonPropertyTargets := []ValueType{
		ValueText, ValueCode, ValueNum, ValueDateTime, ValueDate, ValueTime, ValueUIDRef, ValuePName,
		ValueImage, ValueWaveform, ValueComposite, ValueSCoord, ValueTCoord, ValueContainer,
	}
	if clean(sopClassUID) == Comprehensive3DSRStorage {
		commonPropertyTargets = append(commonPropertyTargets, ValueSCoord3D)
	}
	switch relationship {
	case RelationshipHasObsContext:
		if source == ValueContainer {
			return contains(target, append(commonObservationTargets, ValueContainer)...)
		}
		return contains(source, ValueText, ValueCode, ValueNum) && contains(target, commonObservationTargets...)
	case RelationshipHasAcqContext:
		return contains(source, ValueContainer, ValueImage, ValueWaveform, ValueComposite, ValueNum) &&
			contains(target, ValueText, ValueCode, ValueNum, ValueDateTime, ValueDate, ValueTime, ValueUIDRef, ValuePName, ValueContainer)
	case RelationshipHasProperties:
		if source == ValuePName {
			return contains(target, ValueText, ValueCode, ValueDateTime, ValueDate, ValueTime, ValueUIDRef, ValuePName)
		}
		return contains(source, ValueText, ValueCode, ValueNum) && contains(target, commonPropertyTargets...)
	case RelationshipInferredFrom:
		return contains(source, ValueText, ValueCode, ValueNum) && contains(target, commonPropertyTargets...)
	case RelationshipSelectedFrom:
		if source == ValueSCoord {
			return target == ValueImage
		}
		if source != ValueTCoord {
			return false
		}
		if clean(sopClassUID) == Comprehensive3DSRStorage {
			return contains(target, ValueSCoord, ValueSCoord3D, ValueImage, ValueWaveform)
		}
		return contains(target, ValueSCoord, ValueImage, ValueWaveform)
	default:
		return false
	}
}

func validateEncodedReferenceItem(item ContentItem, maxPathComponents int) error {
	identifier := item.ReferencedContentItemIdentifier
	if len(identifier) == 0 || !identifier.valid(maxPathComponents) {
		return &ReferenceError{Code: ReferenceCodeInvalidPath, Target: identifier.Clone()}
	}
	if item.RelationshipType == "" {
		return &ReferenceError{Code: ReferenceCodeMissingRelation, Target: identifier.Clone()}
	}
	if item.Encoding.ByValueMacroPresent || item.ValueType != "" || !item.ConceptName.Empty() || item.Text != "" || !item.Code.Empty() ||
		item.Measurement != nil || !item.NumericValueQualifier.Empty() || item.DateTime != "" || item.Date != "" ||
		item.Time != "" || item.UID != "" || item.PersonName != "" || hasImageReference(item.Image) ||
		item.Composite.SOPClassUID != "" || item.Composite.SOPInstanceUID != "" ||
		item.Waveform.SOPClassUID != "" || item.Waveform.SOPInstanceUID != "" || len(item.Waveform.Channels) != 0 ||
		item.Spatial.GraphicType != "" || len(item.Spatial.Coordinates) != 0 ||
		item.Spatial3D.GraphicType != "" || len(item.Spatial3D.Coordinates) != 0 ||
		item.Temporal.RangeType != "" || len(item.Temporal.SamplePositions) != 0 || len(item.Temporal.TimeOffsets) != 0 ||
		len(item.Temporal.DateTimes) != 0 || len(item.ValueElements) != 0 || item.ContinuityOfContent != "" || len(item.Children) != 0 {
		return &ReferenceError{Code: ReferenceCodeByValueMacro, Target: identifier.Clone()}
	}
	return nil
}

func detectReferenceCycles(edges []ReferenceEdge, nodes map[string]referenceNode, nodeOrder []string, report *ValidationReport, options ReferenceOptions) {
	adjacency := map[string][]int{}
	for edgeIndex, edge := range edges {
		sourceKey := identifierKey(edge.Source)
		targetKey := identifierKey(edge.Target)
		if _, sourceOK := nodes[sourceKey]; !sourceOK {
			continue
		}
		if _, targetOK := nodes[targetKey]; !targetOK {
			continue
		}
		adjacency[sourceKey] = append(adjacency[sourceKey], edgeIndex)
	}
	type cycleFrame struct {
		node string
		next int
	}
	colors := make(map[string]uint8, len(nodes))
	for _, start := range nodeOrder {
		if colors[start] != 0 {
			continue
		}
		colors[start] = 1
		stack := []cycleFrame{{node: start}}
		for len(stack) > 0 {
			frame := &stack[len(stack)-1]
			edgeIndexes := adjacency[frame.node]
			if frame.next >= len(edgeIndexes) {
				colors[frame.node] = 2
				stack = stack[:len(stack)-1]
				continue
			}
			edge := edges[edgeIndexes[frame.next]]
			frame.next++
			target := identifierKey(edge.Target)
			switch colors[target] {
			case 0:
				colors[target] = 1
				stack = append(stack, cycleFrame{node: target})
			case 1:
				addReferenceFinding(report, options, ReferenceCodeCycle, edge.Slot, edge.Target)
				return
			}
		}
	}
}

func (index *ReferenceIndex) checkCurrent() error {
	if index == nil || index.document == nil {
		return ErrStaleReferenceIndex
	}
	fingerprint, err := fingerprintDocument(index.document, index.options)
	if err != nil || fingerprint != index.fingerprint {
		return ErrStaleReferenceIndex
	}
	return nil
}

// Edges returns a defensive copy of every encoded reference edge.
func (index *ReferenceIndex) Edges() ([]ReferenceEdge, error) {
	if err := index.checkCurrent(); err != nil {
		return nil, err
	}
	out := make([]ReferenceEdge, len(index.edges))
	for edgeIndex := range index.edges {
		out[edgeIndex] = index.edges[edgeIndex].clone()
	}
	return out, nil
}

// TargetsFrom returns the reference relationships encoded as children of the
// source Content Item.
func (index *ReferenceIndex) TargetsFrom(source ContentItemIdentifier) ([]ReferenceEdge, error) {
	if err := index.checkCurrent(); err != nil {
		return nil, err
	}
	return index.edgesFor(index.bySource[identifierKey(source)]), nil
}

// ReferencesTo returns all reverse relationships whose target is target.
func (index *ReferenceIndex) ReferencesTo(target ContentItemIdentifier) ([]ReferenceEdge, error) {
	if err := index.checkCurrent(); err != nil {
		return nil, err
	}
	return index.edgesFor(index.byTarget[identifierKey(target)]), nil
}

// TargetFor returns the encoded target path for one relationship slot.
func (index *ReferenceIndex) TargetFor(slot ContentItemIdentifier) (ContentItemIdentifier, error) {
	if err := index.checkCurrent(); err != nil {
		return nil, err
	}
	edgeIndex, ok := index.bySlot[identifierKey(slot)]
	if !ok {
		return nil, &ReferenceError{Code: ReferenceCodeDangling, Slot: slot.Clone()}
	}
	return index.edges[edgeIndex].Target.Clone(), nil
}

func (index *ReferenceIndex) edgesFor(edgeIndexes []int) []ReferenceEdge {
	out := make([]ReferenceEdge, len(edgeIndexes))
	for resultIndex, edgeIndex := range edgeIndexes {
		out[resultIndex] = index.edges[edgeIndex].clone()
	}
	return out
}

func fingerprintDocument(document *Document, options ReferenceOptions) ([sha256.Size]byte, error) {
	hasher := sha256.New()
	hashString(hasher, document.SOPClassUID)
	root := ContentItemIdentifier{1}
	walker := newReferenceWalker(document.Content, root, 1)
	count := 0
	for {
		entry, ok := walker.next()
		if !ok {
			break
		}
		count++
		if count > options.MaxItems || entry.depth > options.MaxDepth {
			return [sha256.Size]byte{}, ErrResourceLimitExceeded
		}
		hashIdentifier(hasher, entry.path)
		hashContentItem(hasher, entry.item)
		walker.push(entry.item.Children, entry.path, entry.depth+1)
	}
	var result [sha256.Size]byte
	copy(result[:], hasher.Sum(nil))
	return result, nil
}

func hashContentItem(hasher hash.Hash, item *ContentItem) {
	hashString(hasher, string(item.ValueType))
	hashString(hasher, item.RelationshipType)
	hashCode(hasher, item.ConceptName)
	hashString(hasher, item.Text)
	hashCode(hasher, item.Code)
	if item.Measurement != nil {
		hashUint64(hasher, math.Float64bits(item.Measurement.Value))
		hashCode(hasher, item.Measurement.Units)
	}
	hashCode(hasher, item.NumericValueQualifier)
	hashString(hasher, item.DateTime)
	hashString(hasher, item.Date)
	hashString(hasher, item.Time)
	hashString(hasher, item.UID)
	hashString(hasher, item.PersonName)
	hashString(hasher, item.Image.SOPClassUID)
	hashString(hasher, item.Image.SOPInstanceUID)
	hashString(hasher, item.Composite.SOPClassUID)
	hashString(hasher, item.Composite.SOPInstanceUID)
	hashString(hasher, item.ContinuityOfContent)
	hashIdentifier(hasher, item.ReferencedContentItemIdentifier)
	if item.Encoding.ByValueMacroPresent {
		hashUint64(hasher, 1)
	} else {
		hashUint64(hasher, 0)
	}
	if item.Encoding.EmptyReferenceMacroPresent {
		hashUint64(hasher, 1)
	} else {
		hashUint64(hasher, 0)
	}
	hashUint64(hasher, uint64(len(item.Children)))
	for _, element := range item.ValueElements {
		tag := element.Tag()
		hashUint64(hasher, uint64(uint32(tag.Group)<<16|uint32(tag.Element)))
		hashString(hasher, string(element.VR()))
		if raw, ok := element.RawBytes(); ok {
			hashBytes(hasher, raw)
		}
	}
}

func hashCode(hasher hash.Hash, code CodedEntry) {
	hashString(hasher, code.CodeValue)
	hashString(hasher, code.CodingSchemeDesignator)
	hashString(hasher, code.CodeMeaning)
}

func hashIdentifier(hasher hash.Hash, identifier ContentItemIdentifier) {
	hashUint64(hasher, uint64(len(identifier)))
	var raw [4]byte
	for _, component := range identifier {
		binary.BigEndian.PutUint32(raw[:], component)
		_, _ = hasher.Write(raw[:])
	}
}

func hashString(hasher hash.Hash, value string) {
	hashBytes(hasher, []byte(value))
}

func hashBytes(hasher hash.Hash, value []byte) {
	hashUint64(hasher, uint64(len(value)))
	_, _ = hasher.Write(value)
}

func hashUint64(hasher hash.Hash, value uint64) {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], value)
	_, _ = hasher.Write(raw[:])
}
