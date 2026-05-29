package deid

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

type stagedCancelContext struct {
	context.Context
	calls int
}

func (ctx *stagedCancelContext) Err() error {
	ctx.calls++
	if ctx.calls > 1 {
		return context.Canceled
	}
	return nil
}

func TestResolveActionCodeCoversPS315Actions(t *testing.T) {
	tests := []struct {
		code        ActionCode
		requirement AttributeRequirement
		want        ProfileAction
	}{
		{ActionCodeRemove, AttributeOptional, ProfileActionRemove},
		{ActionCodeZero, AttributeType2, ProfileActionZero},
		{ActionCodeDummy, AttributeType1, ProfileActionDummy},
		{ActionCodeClean, AttributeType1, ProfileActionClean},
		{ActionCodeUID, AttributeType1, ProfileActionRemapUID},
		{ActionCodeKeep, AttributeType1, ProfileActionKeep},
		{ActionCodeZeroOrDummy, AttributeType2, ProfileActionZero},
		{ActionCodeZeroOrDummy, AttributeType1, ProfileActionDummy},
		{ActionCodeRemoveOrZero, AttributeOptional, ProfileActionRemove},
		{ActionCodeRemoveOrZero, AttributeType1, ProfileActionZero},
		{ActionCodeRemoveOrZero, AttributeType2, ProfileActionZero},
		{ActionCodeRemoveOrDummy, AttributeOptional, ProfileActionRemove},
		{ActionCodeRemoveOrDummy, AttributeType1, ProfileActionDummy},
		{ActionCodeRemoveZeroOrDummy, AttributeOptional, ProfileActionRemove},
		{ActionCodeRemoveZeroOrDummy, AttributeType2, ProfileActionZero},
		{ActionCodeRemoveZeroOrDummy, AttributeType1, ProfileActionDummy},
		{ActionCodeRemoveZeroOrUID, AttributeOptional, ProfileActionRemove},
		{ActionCodeRemoveZeroOrUID, AttributeType2, ProfileActionZero},
		{ActionCodeRemoveZeroOrUID, AttributeType1, ProfileActionRemapUID},
	}
	for _, test := range tests {
		t.Run(string(test.code)+"/"+test.requirement.String(), func(t *testing.T) {
			got, err := ResolveActionCode(test.code, test.requirement)
			if err != nil {
				t.Fatalf("ResolveActionCode: %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolveActionCode(%q, %s) = %s, want %s", test.code, test.requirement, got, test.want)
			}
		})
	}
	if _, err := ResolveActionCode(ActionCode("BAD"), AttributeType1); !errors.Is(err, ErrInvalidActionCode) {
		t.Fatalf("invalid action error = %v, want ErrInvalidActionCode", err)
	}
}

func TestBasicProfilePlanDryRunAndApplyAreIdenticalAndRedacted(t *testing.T) {
	const phi = "SECRET^PATIENT"
	tagStudyDescription := core.NewTag(0x0008, 0x1030)
	tagReferencedSequence := core.NewTag(0x0008, 0x1115)
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagPatientName, core.VRPN, []byte(phi)),
		core.NewRawElement(tagPatientID, core.VRLO, []byte("SECRET-ID")),
		core.NewRawElement(tagStudyDescription, core.VRLO, []byte("SECRET DESCRIPTION")),
		core.NewRawElement(tagStudyInstanceUID, core.VRUI, []byte("1.2.3")),
		profileSequence(tagReferencedSequence, core.DataSet{Elements: []core.Element{
			core.NewRawElement(tagReferencedSOPUID, core.VRUI, []byte("1.2.4")),
		}}),
	}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanDescriptors}
	options.RequirementResolver = func(context.Context, AttributeContext) (AttributeRequirement, error) {
		return AttributeType1, nil
	}
	options.Cleaner = func(_ context.Context, input CleanContext) (core.Element, error) {
		return core.NewRawElement(input.Tag, input.VR, []byte("SAFE")), nil
	}
	uids := NewUIDRemapper()

	plan, err := PlanBasicProfile(context.Background(), obj, options, uids)
	if err != nil {
		t.Fatalf("PlanBasicProfile: %v", err)
	}
	if value, _ := obj.GetString(tagPatientName); !strings.Contains(value, "SECRET") {
		t.Fatal("dry-run mutated source object")
	}
	dryRun := plan.Report()
	encoded, err := json.Marshal(dryRun)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for _, secret := range []string{phi, "SECRET-ID", "SECRET DESCRIPTION", "1.2.3", "1.2.4"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("redacted report leaked source value %q: %s", secret, encoded)
		}
	}

	applied, err := plan.Apply(context.Background(), obj)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(applied, dryRun) {
		t.Fatalf("apply report differs from dry-run\napply: %#v\ndry: %#v", applied, dryRun)
	}
	if value, _ := obj.GetString(tagPatientName); strings.TrimSpace(value) != "" {
		t.Fatalf("PatientName = %q, want zero length", value)
	}
	if value, _ := obj.GetString(tagPatientID); strings.TrimSpace(value) == "" || strings.Contains(value, "SECRET") {
		t.Fatalf("PatientID = %q, want non-identifying dummy", value)
	}
	if value, _ := obj.GetString(tagStudyDescription); strings.TrimSpace(value) != "SAFE" {
		t.Fatalf("StudyDescription = %q, want SAFE", value)
	}
	if got := trimmedUID(obj, tagStudyInstanceUID); got == "1.2.3" || got == "" {
		t.Fatalf("StudyInstanceUID = %q, want remapped UID", got)
	}
	items, ok := obj.GetSequence(tagReferencedSequence)
	if !ok || len(items) != 1 || trimmedUID(items[0], tagReferencedSOPUID) == "1.2.4" {
		t.Fatal("nested referenced SOP UID was not remapped")
	}
	if value, _ := obj.GetString(core.NewTag(0x0012, 0x0062)); strings.TrimSpace(value) != "YES" {
		t.Fatalf("PatientIdentityRemoved = %q, want YES", value)
	}
	if _, ok := obj.GetSequence(core.NewTag(0x0012, 0x0064)); !ok {
		t.Fatal("missing De-identification Method Code Sequence")
	}
	if _, err := plan.Apply(context.Background(), obj); !errors.Is(err, ErrStaleProfilePlan) {
		t.Fatalf("second Apply error = %v, want ErrStaleProfilePlan", err)
	}
}

func TestBasicProfilePlanRejectsStaleObject(t *testing.T) {
	obj := object.FromElements([]core.Element{core.NewRawElement(tagPatientName, core.VRPN, []byte("ORIGINAL"))}, nil)
	plan, err := PlanBasicProfile(context.Background(), obj, DefaultBasicProfileOptions(), nil)
	if err != nil {
		t.Fatalf("PlanBasicProfile: %v", err)
	}
	obj.Put(core.NewRawElement(tagPatientName, core.VRPN, []byte("CHANGED")))
	if _, err := plan.Apply(context.Background(), obj); !errors.Is(err, ErrStaleProfilePlan) {
		t.Fatalf("Apply stale error = %v, want ErrStaleProfilePlan", err)
	}

	limited := object.FromElements([]core.Element{core.NewRawElement(tagPatientName, core.VRPN, []byte("ORIGINAL"))}, nil)
	options := DefaultBasicProfileOptions()
	options.Limits.MaxElements = 10
	limitedPlan, err := PlanBasicProfile(context.Background(), limited, options, nil)
	if err != nil {
		t.Fatalf("limited PlanBasicProfile: %v", err)
	}
	for index := 0; index < 10; index++ {
		limited.Put(core.NewRawElement(core.NewTag(0x0010, uint16(0x1000+index)), core.VRLO, []byte("A")))
	}
	if _, err := limitedPlan.Apply(context.Background(), limited); !errors.Is(err, ErrProfileResourceLimit) {
		t.Fatalf("Apply over-budget stale object = %v, want ErrProfileResourceLimit", err)
	}

	cancelTarget := object.FromElements([]core.Element{core.NewRawElement(tagPatientName, core.VRPN, []byte("ORIGINAL"))}, nil)
	cancelPlan, err := PlanBasicProfile(context.Background(), cancelTarget, DefaultBasicProfileOptions(), nil)
	if err != nil {
		t.Fatalf("cancel PlanBasicProfile: %v", err)
	}
	staged := &stagedCancelContext{Context: context.Background()}
	if _, err := cancelPlan.Apply(staged, cancelTarget); !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply cancellation before mutation = %v, want context.Canceled", err)
	}
	if value, _ := cancelTarget.GetString(tagPatientName); !strings.Contains(value, "ORIGINAL") {
		t.Fatal("canceled Apply mutated source")
	}
}

func TestModifiedDatesUseOneInjectedShiftWithoutLeakingInputs(t *testing.T) {
	tagAcquisitionDate := core.NewTag(0x0008, 0x0022)
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagStudyDate, core.VRDA, []byte("20240101")),
		core.NewRawElement(tagAcquisitionDate, core.VRDA, []byte("20240103")),
	}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionRetainModifiedDates}
	calls := 0
	options.DateShiftPolicy = func(_ context.Context, input DateShiftContext) (int, error) {
		calls++
		if input.Object == nil {
			t.Fatal("DateShiftContext.Object is nil")
		}
		return 10, nil
	}
	plan, err := PlanBasicProfile(context.Background(), obj, options, nil)
	if err != nil {
		t.Fatalf("PlanBasicProfile: %v", err)
	}
	if calls != 1 {
		t.Fatalf("DateShiftPolicy calls = %d, want 1 per plan", calls)
	}
	if _, err := plan.Apply(context.Background(), obj); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, _ := obj.GetString(tagStudyDate); strings.TrimSpace(got) != "20240111" {
		t.Fatalf("StudyDate = %q, want 20240111", got)
	}
	if got, _ := obj.GetString(tagAcquisitionDate); strings.TrimSpace(got) != "20240113" {
		t.Fatalf("AcquisitionDate = %q, want 20240113", got)
	}
	encoded, _ := json.Marshal(plan.Report())
	for _, forbidden := range []string{"20240101", "20240103", "10"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("date report leaked %q: %s", forbidden, encoded)
		}
	}

	options.DateShiftPolicy = nil
	if _, err := PlanBasicProfile(context.Background(), object.FromElements([]core.Element{
		core.NewRawElement(tagStudyDate, core.VRDA, []byte("20240101")),
	}, nil), options, nil); !errors.Is(err, ErrDateShiftPolicyRequired) {
		t.Fatalf("missing date policy error = %v, want ErrDateShiftPolicyRequired", err)
	}

	options.DateShiftPolicy = func(context.Context, DateShiftContext) (int, error) { return 10, nil }
	if _, err := PlanBasicProfile(context.Background(), object.FromElements([]core.Element{
		core.NewRawElement(tagStudyDate, core.VRDA, []byte("2024")),
	}, nil), options, nil); !errors.Is(err, ErrUnrepresentableDateShift) {
		t.Fatalf("partial date shift error = %v, want ErrUnrepresentableDateShift", err)
	}
	options.DateShiftPolicy = func(context.Context, DateShiftContext) (int, error) { return 0, nil }
	if _, err := PlanBasicProfile(context.Background(), object.FromElements(nil, nil), options, nil); !errors.Is(err, ErrUnrepresentableDateShift) {
		t.Fatalf("zero date shift error = %v, want ErrUnrepresentableDateShift", err)
	}
	options.DateShiftPolicy = func(context.Context, DateShiftContext) (int, error) { return 1, nil }
	deferred := object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: tagStudyDate, VR: core.VRDA, Length: 8, LengthSet: true},
		Value:  nil,
	}}, nil)
	if _, err := PlanBasicProfile(context.Background(), deferred, options, nil); !errors.Is(err, ErrDeferredValueUnavailable) {
		t.Fatalf("deferred date shift error = %v, want ErrDeferredValueUnavailable", err)
	}
}

func TestRetainFullDatesAndUIDsOnlyOverrideNormativeAttributes(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagStudyDate, core.VRDA, []byte("20240101")),
		core.NewRawElement(tagStudyInstanceUID, core.VRUI, []byte("1.2.3")),
		core.NewRawElement(tagPatientName, core.VRPN, []byte("SECRET")),
	}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionRetainFullDates, ProfileOptionRetainUIDs}
	plan, err := PlanBasicProfile(context.Background(), obj, options, nil)
	if err != nil {
		t.Fatalf("PlanBasicProfile: %v", err)
	}
	if _, err := plan.Apply(context.Background(), obj); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, _ := obj.GetString(tagStudyDate); strings.TrimSpace(got) != "20240101" {
		t.Fatalf("StudyDate = %q, want retained", got)
	}
	if got := trimmedUID(obj, tagStudyInstanceUID); got != "1.2.3" {
		t.Fatalf("StudyInstanceUID = %q, want retained", got)
	}
	if got, _ := obj.GetString(tagPatientName); strings.TrimSpace(got) != "" {
		t.Fatalf("PatientName = %q, retain options must not preserve it", got)
	}
}

func TestRetainSafePrivateRequiresVerifiedCreatorRules(t *testing.T) {
	creator := core.NewTag(0x0011, 0x0010)
	safe := core.NewTag(0x0011, 0x1010)
	unknown := core.NewTag(0x0011, 0x1011)
	registry, err := NewSafePrivateRegistry(SafePrivateProvenance{
		Version: "vendor-rules-2026-08", Checksum: strings.Repeat("a", 64),
	}, []SafePrivateRule{{Tag: safe, Creator: "SAFE_CREATOR", VR: core.VRLO, VM: 1, Action: ProfileActionKeep}})
	if err != nil {
		t.Fatalf("NewSafePrivateRegistry: %v", err)
	}
	obj := object.FromElements([]core.Element{
		core.NewRawElement(creator, core.VRLO, []byte("SAFE_CREATOR")),
		core.NewRawElement(safe, core.VRLO, []byte("SAFE")),
		core.NewRawElement(unknown, core.VRLO, []byte("SECRET")),
	}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionRetainSafePrivate}
	options.SafePrivateRegistry = registry
	plan, err := PlanBasicProfile(context.Background(), obj, options, nil)
	if err != nil {
		t.Fatalf("PlanBasicProfile: %v", err)
	}
	if _, err := plan.Apply(context.Background(), obj); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !obj.Has(creator) || !obj.Has(safe) || obj.Has(unknown) {
		t.Fatalf("safe private result: creator=%v safe=%v unknown=%v", obj.Has(creator), obj.Has(safe), obj.Has(unknown))
	}
	if got := plan.Report(); got.SafePrivateVersion != "vendor-rules-2026-08" || got.SafePrivateChecksum != strings.Repeat("a", 64) {
		t.Fatalf("safe-private provenance = %q/%q", got.SafePrivateVersion, got.SafePrivateChecksum)
	}

	mismatched := object.FromElements([]core.Element{
		core.NewRawElement(creator, core.VRLO, []byte("SAFE_CREATOR")),
		core.NewRawElement(safe, core.VRPN, []byte("SECRET^PRIVATE")),
	}, nil)
	mismatchPlan, err := PlanBasicProfile(context.Background(), mismatched, options, nil)
	if err != nil {
		t.Fatalf("shape-mismatch PlanBasicProfile: %v", err)
	}
	if _, err := mismatchPlan.Apply(context.Background(), mismatched); err != nil {
		t.Fatalf("shape-mismatch Apply: %v", err)
	}
	if mismatched.Has(safe) || mismatched.Has(creator) {
		t.Fatal("safe-private VR mismatch was retained")
	}

	options.SafePrivateRegistry = nil
	if _, err := PlanBasicProfile(context.Background(), obj, options, nil); !errors.Is(err, ErrSafePrivateRegistryRequired) {
		t.Fatalf("missing safe-private registry error = %v, want ErrSafePrivateRegistryRequired", err)
	}
}

func TestSafePrivateMultiplicityUsesDICOMVMForOtherVRs(t *testing.T) {
	tag := core.NewTag(0x0011, 0x1010)
	tests := []struct {
		name    string
		element core.Element
		want    int
	}{
		{name: "OW payload is one value", element: core.NewRawElement(tag, core.VROW, []byte{1, 2, 3, 4}), want: 1},
		{name: "OB payload is one value", element: core.NewRawElement(tag, core.VROB, []byte{1, 2, 3, 4}), want: 1},
		{name: "US payload counts words", element: core.NewRawElement(tag, core.VRUS, []byte{1, 0, 2, 0}), want: 2},
		{name: "LO payload counts components", element: core.NewRawElement(tag, core.VRLO, []byte("one\\two")), want: 2},
		{name: "empty payload has no value", element: core.NewRawElement(tag, core.VROW, nil), want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := profileElementVM(test.element); got != test.want {
				t.Fatalf("profileElementVM = %d, want %d", got, test.want)
			}
		})
	}
}

func TestProfileLimitsAndCancellationFailBeforeMutation(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagPatientName, core.VRPN, []byte("SECRET")),
		core.NewRawElement(tagPatientID, core.VRLO, []byte("SECRET")),
	}, nil)
	options := DefaultBasicProfileOptions()
	options.Limits.MaxElements = 1
	if _, err := PlanBasicProfile(context.Background(), obj, options, nil); !errors.Is(err, ErrProfileResourceLimit) {
		t.Fatalf("resource limit error = %v, want ErrProfileResourceLimit", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PlanBasicProfile(ctx, obj, DefaultBasicProfileOptions(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled plan error = %v, want context.Canceled", err)
	}
	if got, _ := obj.GetString(tagPatientName); !strings.Contains(got, "SECRET") {
		t.Fatal("failed planning mutated source")
	}

	callbackCalls := 0
	dateOptions := DefaultBasicProfileOptions()
	dateOptions.Limits.MaxElements = 1
	dateOptions.SelectedOptions = []ProfileOption{ProfileOptionRetainModifiedDates}
	dateOptions.DateShiftPolicy = func(context.Context, DateShiftContext) (int, error) {
		callbackCalls++
		return 1, nil
	}
	if _, err := PlanBasicProfile(context.Background(), obj, dateOptions, nil); !errors.Is(err, ErrProfileResourceLimit) {
		t.Fatalf("pre-callback resource error = %v, want ErrProfileResourceLimit", err)
	}
	if callbackCalls != 0 {
		t.Fatalf("DateShiftPolicy calls before source budget validation = %d, want 0", callbackCalls)
	}
	reportOptions := DefaultBasicProfileOptions()
	reportOptions.Limits.MaxReportBytes = 1
	if _, err := PlanBasicProfile(context.Background(), object.FromElements(nil, nil), reportOptions, nil); !errors.Is(err, ErrProfileResourceLimit) {
		t.Fatalf("incremental report budget error = %v, want ErrProfileResourceLimit", err)
	}
}

func TestProfileValueBudgetAndPanickingCallbacksFailClosed(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagStudyDescription, core.VRLO, bytes.Repeat([]byte{'S'}, 64)),
	}, nil)
	options := DefaultBasicProfileOptions()
	options.Limits.MaxValueBytes = 32
	if _, err := PlanBasicProfile(context.Background(), obj, options, nil); !errors.Is(err, ErrProfileResourceLimit) {
		t.Fatalf("value budget error = %v, want ErrProfileResourceLimit", err)
	}
	if got, _ := obj.GetString(tagStudyDescription); len(got) != 64 {
		t.Fatal("value budget failure mutated source")
	}

	manyEmptyComponents := object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0008), VR: core.VRCS},
		Value:  core.StringValue(make([]string, 2001)),
	}}, nil)
	options = DefaultBasicProfileOptions()
	options.Limits.MaxValueBytes = 1024
	if _, err := PlanBasicProfile(context.Background(), manyEmptyComponents, options, nil); !errors.Is(err, ErrProfileResourceLimit) {
		t.Fatalf("string component value budget error = %v, want ErrProfileResourceLimit", err)
	}

	options = DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanDescriptors}
	options.Cleaner = func(context.Context, CleanContext) (core.Element, error) {
		panic("SECRET CALLBACK PANIC")
	}
	_, err := PlanBasicProfile(context.Background(), obj, options, nil)
	if !errors.Is(err, ErrProfileCallback) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("panic error = %v, want redacted ErrProfileCallback", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelOptions := DefaultBasicProfileOptions()
	cancelOptions.SelectedOptions = []ProfileOption{ProfileOptionCleanPixelData}
	cancelOptions.PixelCleaner = func(context.Context, *object.Object) ([]PixelRegion, error) {
		cancel()
		return nil, context.Canceled
	}
	pixelObject := object.FromElements([]core.Element{core.NewRawElement(tagPixelData, core.VROB, []byte{1, 2})}, nil)
	if _, err := PlanBasicProfile(ctx, pixelObject, cancelOptions, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("callback cancellation error = %v, want context.Canceled", err)
	}
}

func TestCleanPixelAndVisualOptionsOnlyAttestAfterSuccessfulCallbacks(t *testing.T) {
	pixel := []byte{1, 2, 3, 4}
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagPixelData, core.VROB, pixel),
		core.NewRawElement(tagBurnedInAnnotation, core.VRCS, []byte("YES")),
		core.NewRawElement(tagRecognizableVisualFeatures, core.VRCS, []byte("YES")),
	}, nil)
	unattested, err := PlanBasicProfile(context.Background(), obj, DefaultBasicProfileOptions(), nil)
	if err != nil {
		t.Fatalf("unattested PlanBasicProfile: %v", err)
	}
	if report := unattested.Report(); report.Complete || len(report.ResidualRisks) == 0 {
		t.Fatalf("unattested pixel report = complete:%v risks:%v", report.Complete, report.ResidualRisks)
	}
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{
		ProfileOptionCleanPixelData,
		ProfileOptionCleanRecognizableFeatures,
	}
	options.PixelCleaner = func(_ context.Context, clone *object.Object) ([]PixelRegion, error) {
		clone.Put(core.NewRawElement(tagPixelData, core.VROB, []byte{0, 0, 3, 4}))
		return []PixelRegion{{X: 0, Y: 0, Width: 1, Height: 1}}, nil
	}
	options.VisualFeaturesCleaner = func(_ context.Context, clone *object.Object) error {
		clone.Remove(core.NewTag(0x0070, 0x0001))
		return nil
	}
	plan, err := PlanBasicProfile(context.Background(), obj, options, nil)
	if err != nil {
		t.Fatalf("PlanBasicProfile: %v", err)
	}
	if !plan.Report().Complete {
		t.Fatalf("successfully attested pixel/visual report is incomplete: %#v", plan.Report().ResidualRisks)
	}
	if got, _ := obj.GetRaw(tagPixelData); !bytes.Equal(got, pixel) {
		t.Fatal("cleaning dry-run mutated source pixels")
	}
	if _, err := plan.Apply(context.Background(), obj); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, _ := obj.GetRaw(tagPixelData); !bytes.Equal(got, []byte{0, 0, 3, 4}) {
		t.Fatalf("cleaned pixels = %v", got)
	}
	for _, tag := range []core.Tag{tagBurnedInAnnotation, tagRecognizableVisualFeatures} {
		if got, _ := obj.GetString(tag); strings.TrimSpace(got) != "NO" {
			t.Fatalf("attestation %s = %q, want NO", tag, got)
		}
	}
}

func TestProfileCleanersCannotMutateIdentifiersOrExceedBudgets(t *testing.T) {
	newObject := func() *object.Object {
		return object.FromElements([]core.Element{
			core.NewRawElement(tagPatientName, core.VRPN, []byte("ORIGINAL^PATIENT")),
			core.NewRawElement(tagPixelData, core.VROB, []byte{1, 2, 3, 4}),
		}, nil)
	}
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanPixelData}
	options.PixelCleaner = func(_ context.Context, clone *object.Object) ([]PixelRegion, error) {
		clone.Put(core.NewRawElement(tagPatientName, core.VRPN, []byte("REINTRODUCED^PHI")))
		return nil, nil
	}
	obj := newObject()
	if _, err := PlanBasicProfile(context.Background(), obj, options, nil); !errors.Is(err, ErrProfileCallback) {
		t.Fatalf("identifier mutation error = %v, want ErrProfileCallback", err)
	}
	if got, _ := obj.GetString(tagPatientName); !strings.Contains(got, "ORIGINAL") {
		t.Fatal("rejected cleaner mutated source")
	}

	options.PixelCleaner = func(_ context.Context, clone *object.Object) ([]PixelRegion, error) {
		clone.Put(profileSequence(core.NewTag(0x0088, 0x0200), core.DataSet{Elements: []core.Element{
			core.NewRawElement(tagPatientName, core.VRPN, []byte("NESTED^PHI")),
		}}))
		return nil, nil
	}
	if _, err := PlanBasicProfile(context.Background(), newObject(), options, nil); !errors.Is(err, ErrProfileCallback) {
		t.Fatalf("nested identifier mutation error = %v, want ErrProfileCallback", err)
	}

	options.PixelCleaner = func(_ context.Context, clone *object.Object) ([]PixelRegion, error) {
		clone.Put(profileSequence(core.NewTag(0x0088, 0x0200), core.DataSet{Elements: []core.Element{
			core.NewRawElement(tagPatientName, core.VRPN, []byte("SECRET")),
			core.NewRawElement(tagPatientName, core.VRPN, nil),
		}}))
		return nil, nil
	}
	if _, err := PlanBasicProfile(context.Background(), newObject(), options, nil); !errors.Is(err, ErrProfileCallback) {
		t.Fatalf("duplicate nested identifier mutation error = %v, want ErrProfileCallback", err)
	}

	visualSequence := core.NewTag(0x0070, 0x0008)
	visualObject := object.FromElements([]core.Element{profileSequence(visualSequence, core.DataSet{})}, nil)
	visualOptions := DefaultBasicProfileOptions()
	visualOptions.SelectedOptions = []ProfileOption{ProfileOptionCleanRecognizableFeatures}
	visualOptions.VisualFeaturesCleaner = func(_ context.Context, clone *object.Object) error {
		element, _ := clone.Get(visualSequence)
		element.Header.VR = core.VRUN
		clone.Put(element)
		return nil
	}
	if _, err := PlanBasicProfile(context.Background(), visualObject, visualOptions, nil); !errors.Is(err, ErrProfileCallback) {
		t.Fatalf("sequence VR mutation error = %v, want ErrProfileCallback", err)
	}

	deferredOutput := object.FromElements(nil, nil)
	visualOptions.VisualFeaturesCleaner = func(_ context.Context, clone *object.Object) error {
		clone.Put(core.Element{Header: core.ElementHeader{Tag: tagPixelData, VR: core.VROB, Length: 4, LengthSet: true}})
		return nil
	}
	if _, err := PlanBasicProfile(context.Background(), deferredOutput, visualOptions, nil); !errors.Is(err, ErrDeferredValueUnavailable) {
		t.Fatalf("callback deferred output error = %v, want ErrDeferredValueUnavailable", err)
	}

	options.Limits.MaxValueBytes = 32
	options.PixelCleaner = func(_ context.Context, clone *object.Object) ([]PixelRegion, error) {
		clone.Put(core.NewRawElement(tagPixelData, core.VROB, make([]byte, 64)))
		return nil, nil
	}
	if _, err := PlanBasicProfile(context.Background(), newObject(), options, nil); !errors.Is(err, ErrProfileResourceLimit) {
		t.Fatalf("callback output budget error = %v, want ErrProfileResourceLimit", err)
	}
}

func TestProfileVisualCleanerRejectsNestedDeferredValues(t *testing.T) {
	graphicSequence := core.NewTag(0x0070, 0x9999)
	obj := object.FromElements([]core.Element{profileSequence(graphicSequence, core.DataSet{Elements: []core.Element{{
		Header: core.ElementHeader{Tag: core.NewTag(0x0070, 0x9998), VR: core.VRST, Length: 32, LengthSet: true},
		Value:  nil,
	}}})}, nil)
	called := false
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanRecognizableFeatures}
	options.VisualFeaturesCleaner = func(context.Context, *object.Object) error {
		called = true
		return nil
	}
	if _, err := PlanBasicProfile(context.Background(), obj, options, nil); !errors.Is(err, ErrDeferredValueUnavailable) {
		t.Fatalf("nested deferred value error = %v, want ErrDeferredValueUnavailable", err)
	}
	if called {
		t.Fatal("VisualFeaturesCleaner called with nested deferred value")
	}
}

func TestProfilePixelCleanerReceivesSourceByteOrder(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagPixelData, core.VROW, []byte{0, 1}),
	}, nil)
	obj.SetValueByteOrder(binary.BigEndian)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanPixelData}
	options.PixelCleaner = func(_ context.Context, clone *object.Object) ([]PixelRegion, error) {
		if clone.ValueByteOrder() != binary.BigEndian {
			t.Fatal("PixelCleaner clone lost source byte order")
		}
		return nil, nil
	}
	plan, err := PlanBasicProfile(context.Background(), obj, options, nil)
	if err != nil {
		t.Fatalf("PlanBasicProfile: %v", err)
	}
	obj.SetValueByteOrder(binary.LittleEndian)
	if _, err := plan.Apply(context.Background(), obj); !errors.Is(err, ErrStaleProfilePlan) {
		t.Fatalf("Apply after byte-order change = %v, want ErrStaleProfilePlan", err)
	}
}

func TestProfilePixelCleanerRejectsUnavailableDeferredPixelData(t *testing.T) {
	obj := object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: tagPixelData, VR: core.VROB, Length: 1024, LengthSet: true},
		Value:  nil,
	}}, nil)
	called := false
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanPixelData}
	options.PixelCleaner = func(context.Context, *object.Object) ([]PixelRegion, error) {
		called = true
		return nil, nil
	}
	if _, err := PlanBasicProfile(context.Background(), obj, options, nil); !errors.Is(err, ErrDeferredValueUnavailable) {
		t.Fatalf("deferred Pixel Data error = %v, want ErrDeferredValueUnavailable", err)
	}
	if called {
		t.Fatal("PixelCleaner called without materialized Pixel Data")
	}
}

func TestBasicProfileDeferredValuesRespectActionSemantics(t *testing.T) {
	deferred := core.Element{
		Header: core.ElementHeader{Tag: tagPatientName, VR: core.VRPN, Length: 1024, LengthSet: true},
		Value:  nil,
	}
	obj := object.FromElements([]core.Element{deferred}, nil)
	options := DefaultBasicProfileOptions()
	options.Limits.MaxValueBytes = 2048
	plan, err := PlanBasicProfile(context.Background(), obj, options, nil)
	if err != nil {
		t.Fatalf("PlanBasicProfile deferred zero action: %v", err)
	}
	if _, err := plan.Apply(context.Background(), obj); err != nil {
		t.Fatalf("Apply deferred zero action: %v", err)
	}
	if value, ok := obj.GetRaw(tagPatientName); !ok || len(value) != 0 {
		t.Fatalf("zeroed deferred PatientName = %v, %v", value, ok)
	}

	uid := core.Element{Header: core.ElementHeader{Tag: tagStudyInstanceUID, VR: core.VRUI, Length: 8, LengthSet: true}}
	uidObj := object.FromElements([]core.Element{uid}, nil)
	if _, err := PlanBasicProfile(context.Background(), uidObj, DefaultBasicProfileOptions(), nil); !errors.Is(err, ErrDeferredValueUnavailable) {
		t.Fatalf("PlanBasicProfile deferred UID error = %v, want ErrDeferredValueUnavailable", err)
	}
}

func TestCloneFileWithBasicProfileRebuildsMetaAndPreamble(t *testing.T) {
	source := uidOnlyFile("1.2.3", "1.2.4", "1.2.5")
	source.Preamble = bytes.Repeat([]byte("S"), 128)
	clone, report, err := CloneFileWithBasicProfile(context.Background(), source, DefaultBasicProfileOptions(), nil)
	if err != nil {
		t.Fatalf("CloneFileWithBasicProfile: %v", err)
	}
	if !report.Complete || len(clone.Preamble) != 0 {
		t.Fatalf("clone report/preamble = complete:%v len:%d", report.Complete, len(clone.Preamble))
	}
	if !bytes.Equal(source.Preamble, bytes.Repeat([]byte("S"), 128)) {
		t.Fatal("source preamble was mutated")
	}
	datasetUID, datasetOK := clone.Dataset.GetUID(tagSOPInstanceUID)
	metaUID, metaOK := clone.Meta.GetUID(testTagMediaStorageSOPInstanceUIDForClone)
	if !datasetOK || !metaOK || datasetUID != metaUID {
		t.Fatalf("dataset/meta SOP Instance UID = %q/%q", datasetUID, metaUID)
	}
}

func TestNormativeProfileOptionsAffectOnlyTheirColumns(t *testing.T) {
	patientSex := core.NewTag(0x0010, 0x0040)
	deviceSerial := core.NewTag(0x0018, 0x1000)
	institutionName := core.NewTag(0x0008, 0x0080)
	contentSequence := core.NewTag(0x0040, 0xA730)
	overlayData := core.NewTag(0x6002, 0x3000)
	obj := object.FromElements([]core.Element{
		core.NewRawElement(patientSex, core.VRCS, []byte("F")),
		core.NewRawElement(deviceSerial, core.VRLO, []byte("DEVICE-SECRET")),
		core.NewRawElement(institutionName, core.VRLO, []byte("INSTITUTION-SECRET")),
		profileSequence(contentSequence, core.DataSet{Elements: []core.Element{
			core.NewRawElement(tagContentValueType, core.VRCS, []byte("TEXT")),
			profileSequence(tagConceptNameCodeSequence, core.DataSet{Elements: []core.Element{
				core.NewRawElement(tagCodeValue, core.VRSH, []byte("120999")),
				core.NewRawElement(tagCodingSchemeDesignator, core.VRSH, []byte("DCM")),
			}}),
			core.NewRawElement(tagStructuredTextValue, core.VRUT, []byte("SAFE DEVICE LABEL")),
			core.NewRawElement(tagPatientName, core.VRPN, []byte("SECRET^NESTED")),
		}}),
		core.NewRawElement(overlayData, core.VROW, []byte{1, 2, 3, 4}),
	}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{
		ProfileOptionRetainPatientCharacteristics,
		ProfileOptionRetainDeviceIdentity,
		ProfileOptionRetainInstitutionIdentity,
		ProfileOptionCleanStructuredContent,
		ProfileOptionCleanGraphics,
	}
	options.Cleaner = func(_ context.Context, input CleanContext) (core.Element, error) {
		if input.Tag != overlayData {
			t.Fatalf("unexpected scalar clean tag %s", input.Tag)
		}
		return core.NewRawElement(input.Tag, input.VR, []byte{0, 0, 0, 0}), nil
	}
	report, err := ApplyBasicProfile(context.Background(), obj, options, nil)
	if err != nil {
		t.Fatalf("ApplyBasicProfile: %v", err)
	}
	for tag, want := range map[core.Tag]string{
		patientSex:      "F",
		deviceSerial:    "DEVICE-SECRET",
		institutionName: "INSTITUTION-SECRET",
	} {
		if got, _ := obj.GetString(tag); strings.TrimSpace(got) != want {
			t.Fatalf("retained %s = %q, want %q", tag, got, want)
		}
	}
	items, ok := obj.GetSequence(contentSequence)
	if !ok || len(items) != 1 {
		t.Fatal("Clean Structured Content removed Content Sequence")
	}
	if got, _ := items[0].GetString(tagPatientName); strings.TrimSpace(got) != "" {
		t.Fatalf("nested PatientName = %q", got)
	}
	if got, _ := obj.GetRaw(overlayData); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
		t.Fatalf("cleaned overlay = %v", got)
	}
	wantOptions := []ProfileOption{
		ProfileOptionCleanGraphics,
		ProfileOptionCleanStructuredContent,
		ProfileOptionRetainPatientCharacteristics,
		ProfileOptionRetainDeviceIdentity,
		ProfileOptionRetainInstitutionIdentity,
	}
	if !reflect.DeepEqual(report.Options, wantOptions) {
		t.Fatalf("reported options = %v, want %v", report.Options, wantOptions)
	}
}

func TestProfileReportsAndAvoidsUIDMintCollisions(t *testing.T) {
	oldRandRead := randRead
	randRead = func(buffer []byte) (int, error) {
		clear(buffer)
		return len(buffer), nil
	}
	defer func() { randRead = oldRandRead }()

	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagStudyInstanceUID, core.VRUI, []byte("1.2.3")),
		core.NewRawElement(tagSeriesInstanceUID, core.VRUI, []byte("1.2.4")),
	}, nil)
	report, err := ApplyBasicProfile(context.Background(), obj, DefaultBasicProfileOptions(), NewUIDRemapper())
	if err != nil {
		t.Fatalf("ApplyBasicProfile: %v", err)
	}
	if report.Summary.UIDCollisions != 1 {
		t.Fatalf("UIDCollisions = %d, want 1", report.Summary.UIDCollisions)
	}
	if study, series := trimmedUID(obj, tagStudyInstanceUID), trimmedUID(obj, tagSeriesInstanceUID); study == series {
		t.Fatalf("colliding source UIDs mapped to %q", study)
	}
}

func TestPlanDoesNotMutateCallerOptionSlice(t *testing.T) {
	selected := []ProfileOption{ProfileOptionRetainUIDs, ProfileOptionCleanDescriptors}
	want := append([]ProfileOption(nil), selected...)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = selected
	if _, err := PlanBasicProfile(context.Background(), object.FromElements(nil, nil), options, nil); err != nil {
		t.Fatalf("PlanBasicProfile: %v", err)
	}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("SelectedOptions mutated to %v, want %v", selected, want)
	}
}

func TestSequenceDummyRequiresIODAwareProvider(t *testing.T) {
	personIdentification := core.NewTag(0x0040, 0x1101)
	obj := object.FromElements([]core.Element{
		profileSequence(personIdentification, core.DataSet{Elements: []core.Element{
			core.NewRawElement(tagCodeValue, core.VRSH, []byte("SECRET")),
		}}),
	}, nil)
	if _, err := PlanBasicProfile(context.Background(), obj, DefaultBasicProfileOptions(), nil); !errors.Is(err, ErrDummyValueProviderRequired) {
		t.Fatalf("sequence dummy error = %v, want ErrDummyValueProviderRequired", err)
	}
	options := DefaultBasicProfileOptions()
	options.DummyValueProvider = func(_ context.Context, input DummyContext) (core.Element, error) {
		return profileSequence(input.Tag, profileMethodCode("ANON", "Anonymized")), nil
	}
	if _, err := ApplyBasicProfile(context.Background(), obj, options, nil); err != nil {
		t.Fatalf("ApplyBasicProfile with dummy provider: %v", err)
	}
	items, ok := obj.GetSequence(personIdentification)
	if !ok || len(items) != 1 {
		t.Fatal("IOD-aware sequence dummy was not installed")
	}
}

func TestCleanStructuredContentUsesConceptAndValueTypeTable(t *testing.T) {
	contentSequence := core.NewTag(0x0040, 0xA730)
	valueType := core.NewTag(0x0040, 0xA040)
	conceptName := core.NewTag(0x0040, 0xA043)
	textValue := core.NewTag(0x0040, 0xA160)
	referencedSOPSequence := core.NewTag(0x0008, 0x1199)
	referencedSOPClassUID := core.NewTag(0x0008, 0x1150)

	concept := func(code string) core.Element {
		return profileSequence(conceptName, core.DataSet{Elements: []core.Element{
			core.NewRawElement(tagCodeValue, core.VRSH, []byte(code)),
			core.NewRawElement(tagCodingSchemeDesignator, core.VRSH, []byte("DCM")),
		}})
	}
	reference := func(instanceUID string) core.Element {
		return profileSequence(referencedSOPSequence, core.DataSet{Elements: []core.Element{
			core.NewRawElement(referencedSOPClassUID, core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.7")),
			core.NewRawElement(tagReferencedSOPUID, core.VRUI, []byte(instanceUID)),
		}})
	}

	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagSOPClassUID, core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.88.33")),
		profileSequence(contentSequence,
			core.DataSet{Elements: []core.Element{
				core.NewRawElement(valueType, core.VRCS, []byte("TEXT")),
				concept("121022"),
				core.NewRawElement(textValue, core.VRUT, []byte("SECRET ACCESSION")),
			}},
			core.DataSet{Elements: []core.Element{
				core.NewRawElement(valueType, core.VRCS, []byte("TEXT")),
				concept("125203"),
				core.NewRawElement(textValue, core.VRUT, []byte("SECRET DESCRIPTOR")),
			}},
			core.DataSet{Elements: []core.Element{
				core.NewRawElement(valueType, core.VRCS, []byte("IMAGE")),
				concept("121112"),
				reference("1.2.3.4.5"),
			}},
			core.DataSet{Elements: []core.Element{
				core.NewRawElement(valueType, core.VRCS, []byte("WAVEFORM")),
				concept("121112"),
				reference("1.2.3.4.6"),
			}},
		),
	}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanStructuredContent, ProfileOptionCleanDescriptors}
	options.Cleaner = func(_ context.Context, input CleanContext) (core.Element, error) {
		if input.Tag != textValue {
			t.Fatalf("cleaner tag = %s, want Text Value", input.Tag)
		}
		return core.NewRawElement(input.Tag, input.VR, []byte("SAFE")), nil
	}

	report, err := ApplyBasicProfile(context.Background(), obj, options, NewUIDRemapper())
	if err != nil {
		t.Fatalf("ApplyBasicProfile: %v", err)
	}
	items, ok := obj.GetSequence(contentSequence)
	if !ok || len(items) != 2 {
		t.Fatalf("Content Sequence items = %d, ok=%v; want cleaned TEXT and dummy IMAGE", len(items), ok)
	}
	if got, _ := items[0].GetString(textValue); strings.TrimSpace(got) != "SAFE" {
		t.Fatalf("cleaned Text Value = %q, want SAFE", got)
	}
	refs, ok := items[1].GetSequence(referencedSOPSequence)
	if !ok || len(refs) != 1 {
		t.Fatal("dummy IMAGE lost Referenced SOP Sequence")
	}
	if got := trimmedUID(refs[0], tagReferencedSOPUID); got == "" || got == "1.2.3.4.5" {
		t.Fatalf("dummy IMAGE SOP Instance UID = %q, want remapped non-empty UID", got)
	}
	if report.StructuredContentSource != GeneratedStructuredContentSourceURL ||
		report.StructuredContentChecksum != GeneratedStructuredContentProjectionSHA256 {
		t.Fatalf("structured-content provenance = %q/%q", report.StructuredContentSource, report.StructuredContentChecksum)
	}
	encoded, _ := json.Marshal(report)
	for _, secret := range []string{"SECRET ACCESSION", "SECRET DESCRIPTOR", "1.2.3.4.5", "1.2.3.4.6"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("structured-content report leaked %q", secret)
		}
	}
}

func TestCleanStructuredContentAppliesRetainUIDOverrideByValueType(t *testing.T) {
	contentSequence := core.NewTag(0x0040, 0xA730)
	valueType := core.NewTag(0x0040, 0xA040)
	conceptName := core.NewTag(0x0040, 0xA043)
	referencedSOPSequence := core.NewTag(0x0008, 0x1199)
	concept := profileSequence(conceptName, core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagCodeValue, core.VRSH, []byte("371524004")),
		core.NewRawElement(tagCodingSchemeDesignator, core.VRSH, []byte("SCT")),
	}})
	uidConcept := profileSequence(conceptName, core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagCodeValue, core.VRSH, []byte("112357")),
		core.NewRawElement(tagCodingSchemeDesignator, core.VRSH, []byte("DCM")),
	}})
	obj := object.FromElements([]core.Element{profileSequence(contentSequence,
		core.DataSet{Elements: []core.Element{
			core.NewRawElement(valueType, core.VRCS, []byte("COMPOSITE")),
			concept,
			profileSequence(referencedSOPSequence, core.DataSet{Elements: []core.Element{
				core.NewRawElement(core.NewTag(0x0008, 0x1150), core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.7")),
				core.NewRawElement(tagReferencedSOPUID, core.VRUI, []byte("1.2.3.4.5")),
			}}),
		}},
		core.DataSet{Elements: []core.Element{
			core.NewRawElement(valueType, core.VRCS, []byte("TEXT")),
			concept,
			core.NewRawElement(core.NewTag(0x0040, 0xA160), core.VRUT, []byte("SECRET")),
		}},
		core.DataSet{Elements: []core.Element{
			core.NewRawElement(valueType, core.VRCS, []byte("UIDREF")),
			uidConcept,
			core.NewRawElement(core.NewTag(0x0040, 0xA124), core.VRUI, []byte("1.2.3.4.7")),
		}},
	)}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanStructuredContent, ProfileOptionRetainUIDs}
	if _, err := ApplyBasicProfile(context.Background(), obj, options, nil); err != nil {
		t.Fatalf("ApplyBasicProfile: %v", err)
	}
	items, ok := obj.GetSequence(contentSequence)
	if !ok || len(items) != 2 {
		t.Fatalf("Content Sequence items = %d, ok=%v; want retained COMPOSITE and UIDREF", len(items), ok)
	}
	refs, ok := items[0].GetSequence(referencedSOPSequence)
	if !ok || len(refs) != 1 || trimmedUID(refs[0], tagReferencedSOPUID) != "1.2.3.4.5" {
		t.Fatal("Retain UIDs did not keep the matching COMPOSITE reference")
	}
	if uid, _ := items[1].GetString(core.NewTag(0x0040, 0xA124)); strings.TrimSpace(uid) != "1.2.3.4.7" {
		t.Fatalf("Retain UIDs UIDREF = %q, want original", uid)
	}
}

func TestCleanStructuredContentRecognizesRetiredCodeAliases(t *testing.T) {
	concept := profileSequence(tagConceptNameCodeSequence, core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagCodeValue, core.VRSH, []byte("R-42B89")),
		core.NewRawElement(tagCodingSchemeDesignator, core.VRSH, []byte("SRT")),
	}})
	obj := object.FromElements([]core.Element{profileSequence(tagContentSequence, core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagContentValueType, core.VRCS, []byte("COMPOSITE")),
		concept,
		profileSequence(tagReferencedSOPSequence, core.DataSet{Elements: []core.Element{
			core.NewRawElement(core.NewTag(0x0008, 0x1150), core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.7")),
			core.NewRawElement(tagReferencedSOPUID, core.VRUI, []byte("1.2.3.4.5")),
		}}),
	}})}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanStructuredContent, ProfileOptionRetainUIDs}
	report, err := ApplyBasicProfile(context.Background(), obj, options, nil)
	if err != nil {
		t.Fatalf("ApplyBasicProfile retired alias: %v", err)
	}
	items, ok := obj.GetSequence(tagContentSequence)
	if !ok || len(items) != 1 {
		t.Fatalf("retired alias Content Sequence items = %d, ok=%v", len(items), ok)
	}
	refs, ok := items[0].GetSequence(tagReferencedSOPSequence)
	if !ok || len(refs) != 1 || trimmedUID(refs[0], tagReferencedSOPUID) != "1.2.3.4.5" {
		t.Fatal("retired SRT alias did not inherit the current SCT Retain UIDs action")
	}
	if report.RetiredCodeSource != GeneratedRetiredCodeSourceURL || report.RetiredCodeChecksum != GeneratedRetiredCodeProjectionSHA256 {
		t.Fatalf("retired-code provenance = %q/%q", report.RetiredCodeSource, report.RetiredCodeChecksum)
	}
}

func TestCleanStructuredContentRejectsUnclassifiedItemsFailClosed(t *testing.T) {
	contentSequence := core.NewTag(0x0040, 0xA730)
	obj := object.FromElements([]core.Element{profileSequence(contentSequence, core.DataSet{Elements: []core.Element{
		core.NewRawElement(core.NewTag(0x0040, 0xA160), core.VRUT, []byte("UNCLASSIFIED SECRET")),
	}})}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanStructuredContent}
	if _, err := ApplyBasicProfile(context.Background(), obj, options, nil); !errors.Is(err, ErrUnclassifiedStructuredContent) {
		t.Fatalf("unclassified structured-content error = %v, want ErrUnclassifiedStructuredContent", err)
	}
	items, ok := obj.GetSequence(contentSequence)
	if !ok || len(items) != 1 {
		t.Fatal("failed structured-content plan mutated source")
	}
}

func TestCleanStructuredContentKeepsValueLessContainerAndCleansChildren(t *testing.T) {
	contentSequence := core.NewTag(0x0040, 0xA730)
	concept := func(code string) core.Element {
		return profileSequence(tagConceptNameCodeSequence, core.DataSet{Elements: []core.Element{
			core.NewRawElement(tagCodeValue, core.VRSH, []byte(code)),
			core.NewRawElement(tagCodingSchemeDesignator, core.VRSH, []byte("DCM")),
		}})
	}
	child := core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagContentValueType, core.VRCS, []byte("TEXT")),
		concept("121022"),
		core.NewRawElement(tagStructuredTextValue, core.VRUT, []byte("SECRET")),
	}}
	container := core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagContentValueType, core.VRCS, []byte("CONTAINER")),
		concept("121000"),
		profileSequence(contentSequence, child),
	}}
	obj := object.FromElements([]core.Element{profileSequence(contentSequence, container)}, nil)
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanStructuredContent, ProfileOptionRetainDeviceIdentity}
	if _, err := ApplyBasicProfile(context.Background(), obj, options, nil); err != nil {
		t.Fatalf("ApplyBasicProfile: %v", err)
	}
	items, ok := obj.GetSequence(contentSequence)
	if !ok || len(items) != 1 {
		t.Fatalf("root Content Sequence items = %d, ok=%v", len(items), ok)
	}
	children, ok := items[0].GetSequence(contentSequence)
	if !ok || len(children) != 0 {
		t.Fatalf("retained CONTAINER children = %d, ok=%v; want empty", len(children), ok)
	}
}

func TestCleanStructuredContentCoversAcquisitionAndSpecimenSequences(t *testing.T) {
	structuredItem := func() core.DataSet {
		return core.DataSet{Elements: []core.Element{
			core.NewRawElement(tagContentValueType, core.VRCS, []byte("TEXT")),
			profileSequence(tagConceptNameCodeSequence, core.DataSet{Elements: []core.Element{
				core.NewRawElement(tagCodeValue, core.VRSH, []byte("121022")),
				core.NewRawElement(tagCodingSchemeDesignator, core.VRSH, []byte("DCM")),
			}}),
			core.NewRawElement(tagStructuredTextValue, core.VRUT, []byte("SECRET")),
		}}
	}
	options := DefaultBasicProfileOptions()
	options.SelectedOptions = []ProfileOption{ProfileOptionCleanStructuredContent}

	t.Run("acquisition context", func(t *testing.T) {
		obj := object.FromElements([]core.Element{profileSequence(tagAcquisitionContextSequence, structuredItem())}, nil)
		report, err := ApplyBasicProfile(context.Background(), obj, options, nil)
		if err != nil {
			t.Fatalf("ApplyBasicProfile: %v", err)
		}
		items, ok := obj.GetSequence(tagAcquisitionContextSequence)
		if !ok || len(items) != 0 {
			t.Fatalf("Acquisition Context items = %d, ok=%v; want empty", len(items), ok)
		}
		found := false
		for _, action := range report.Actions {
			if action.Reason == "structured-content-table" {
				found = action.Tag == tagAcquisitionContextSequence && len(action.Path) == 1 && action.Path[0].SequenceTag == tagAcquisitionContextSequence
			}
		}
		if !found {
			t.Fatal("Acquisition Context structured action has incorrect tag/path")
		}
	})

	t.Run("specimen preparation step content", func(t *testing.T) {
		obj := object.FromElements([]core.Element{profileSequence(tagSpecimenPreparationSequence, core.DataSet{Elements: []core.Element{
			profileSequence(tagSpecimenPreparationContentSequence, structuredItem()),
		}})}, nil)
		if _, err := ApplyBasicProfile(context.Background(), obj, options, nil); err != nil {
			t.Fatalf("ApplyBasicProfile: %v", err)
		}
		steps, ok := obj.GetSequence(tagSpecimenPreparationSequence)
		if !ok || len(steps) != 1 {
			t.Fatalf("Specimen Preparation steps = %d, ok=%v", len(steps), ok)
		}
		content, ok := steps[0].GetSequence(tagSpecimenPreparationContentSequence)
		if !ok || len(content) != 0 {
			t.Fatalf("Specimen Preparation content items = %d, ok=%v; want empty", len(content), ok)
		}
	})
}

func profileSequence(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.SequenceValue{Items: items},
	}
}
