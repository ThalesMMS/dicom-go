package validation

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
)

type HookPoint string

const (
	HookElementHeaderRead HookPoint = "element_header_read"
	HookAfterElement      HookPoint = "element_decoded"
	HookItemComplete      HookPoint = "item_complete"
	HookSequenceComplete  HookPoint = "sequence_complete"
	HookDataSetComplete   HookPoint = "dataset_complete"
	HookPreValidation     HookPoint = "pre_validation"
	HookPostValidation    HookPoint = "post_validation"
	HookPreSerialization  HookPoint = "pre_serialization"
	HookPostWrite         HookPoint = "post_write"
)

type HookEvent struct {
	Point         HookPoint
	Path          Path
	Header        *core.ElementHeader
	Element       *core.Element
	DataSet       *core.DataSet
	Offset        int64
	OffsetSet     bool
	BytesWritten  int64
	WriteComplete bool
}

type HookDecision struct {
	Element    *core.Element
	Filter     bool
	SkipValue  bool
	DeferValue bool
	Findings   []Finding
}

type Hook interface {
	HandleValidationHook(context.Context, HookEvent) (HookDecision, error)
}

type HookFunc func(context.Context, HookEvent) (HookDecision, error)

func (f HookFunc) HandleValidationHook(ctx context.Context, event HookEvent) (HookDecision, error) {
	if f == nil {
		return HookDecision{}, nil
	}
	return f(ctx, event)
}

type HookFailurePolicy uint8

const (
	HookFailureReject HookFailurePolicy = iota
	HookFailureFinding
)

type HookRegistration struct {
	Name           string
	Points         []HookPoint
	Hook           Hook
	ConcurrentSafe bool
	Timeout        time.Duration
	OnFailure      HookFailurePolicy
}

type registeredHook struct {
	registration HookRegistration
	points       map[HookPoint]struct{}
	gate         chan struct{}
}

type HookChain struct {
	mu    sync.RWMutex
	hooks []*registeredHook
}

var hookNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func NewHookChain(registrations ...HookRegistration) (*HookChain, error) {
	chain := &HookChain{}
	for _, registration := range registrations {
		if err := chain.Add(registration); err != nil {
			return nil, err
		}
	}
	return chain, nil
}

func (c *HookChain) Add(registration HookRegistration) error {
	if c == nil {
		return fmt.Errorf("%w: nil hook chain", ErrInvalidPolicy)
	}
	registration.Name = strings.TrimSpace(registration.Name)
	if !hookNamePattern.MatchString(registration.Name) || registration.Hook == nil || len(registration.Points) == 0 ||
		registration.Timeout < 0 || registration.OnFailure > HookFailureFinding {
		return fmt.Errorf("%w: invalid hook registration", ErrInvalidPolicy)
	}
	points := make(map[HookPoint]struct{}, len(registration.Points))
	for _, point := range registration.Points {
		if !validHookPoint(point) {
			return fmt.Errorf("%w: unknown hook point %q", ErrInvalidPolicy, point)
		}
		points[point] = struct{}{}
	}
	hook := &registeredHook{registration: registration, points: points}
	if !registration.ConcurrentSafe {
		hook.gate = make(chan struct{}, 1)
		hook.gate <- struct{}{}
	}
	c.mu.Lock()
	for _, existing := range c.hooks {
		if existing.registration.Name == registration.Name {
			c.mu.Unlock()
			return fmt.Errorf("%w: duplicate hook name", ErrInvalidPolicy)
		}
	}
	c.hooks = append(c.hooks, hook)
	c.mu.Unlock()
	return nil
}

func (c *HookChain) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.hooks)
}

func (c *HookChain) snapshot() *HookChain {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return &HookChain{hooks: append([]*registeredHook(nil), c.hooks...)}
}

type HookResult struct {
	Element    *core.Element
	Filter     bool
	SkipValue  bool
	DeferValue bool
	Findings   []Finding
	Changes    []Change
}

func (c *HookChain) Run(ctx context.Context, event HookEvent) (HookResult, error) {
	return c.run(ctx, event, false)
}

func (c *HookChain) run(ctx context.Context, event HookEvent, stopAfterFinding bool) (HookResult, error) {
	if c == nil {
		return HookResult{Element: cloneElementPtr(event.Element)}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.RLock()
	hooks := append([]*registeredHook(nil), c.hooks...)
	c.mu.RUnlock()

	working := cloneHookEvent(event)
	result := HookResult{Element: cloneElementPtr(working.Element)}
	for _, registered := range hooks {
		if _, ok := registered.points[event.Point]; !ok {
			continue
		}
		hookCtx := ctx
		cancel := func() {}
		if registered.registration.Timeout > 0 {
			hookCtx, cancel = context.WithTimeout(ctx, registered.registration.Timeout)
		}
		if err := acquireHook(hookCtx, registered); err != nil {
			cancel()
			return c.hookFailure(result, registered, event, CodeHookTimeout, err)
		}
		decision, code, err := invokeHook(hookCtx, registered, working)
		releaseHook(registered)
		cancel()
		if err != nil {
			var handledErr error
			result, handledErr = c.hookFailure(result, registered, event, code, err)
			if handledErr != nil {
				return result, handledErr
			}
			if stopAfterFinding && len(result.Findings) > 0 {
				break
			}
			continue
		}
		if !validHookDecision(event.Point, decision) {
			result, err = c.hookFailure(result, registered, event, CodeHookAction, ErrHookAction)
			if err != nil {
				return result, err
			}
			if stopAfterFinding && len(result.Findings) > 0 {
				break
			}
			continue
		}
		if (decision.SkipValue && result.DeferValue) || (decision.DeferValue && result.SkipValue) {
			result, err = c.hookFailure(result, registered, event, CodeHookAction, ErrHookAction)
			if err != nil {
				return result, err
			}
			if stopAfterFinding && len(result.Findings) > 0 {
				break
			}
			continue
		}
		for _, finding := range decision.Findings {
			finding.Path = event.Path.Clone()
			finding.Rule = ""
			finding.ExpectedVR = nil
			finding.Offset = 0
			finding.OffsetSet = false
			if event.Element != nil {
				finding.Tag = event.Element.Tag()
				finding.VR = event.Element.VR()
			} else if event.Header != nil {
				finding.Tag = event.Header.Tag
				finding.VR = event.Header.VR
			}
			finding.Offset = event.Offset
			finding.OffsetSet = event.OffsetSet
			finding.Hook = registered.registration.Name
			finding.Code = CodeHookDiagnostic
			finding.Message = "hook reported a validation finding"
			if finding.Severity == "" {
				finding.Severity = SeverityWarning
			}
			result.Findings = append(result.Findings, finding)
		}
		if decision.Element != nil {
			replacement := cloneElement(*decision.Element)
			working.Element = &replacement
			result.Element = &replacement
			result.Changes = append(result.Changes, Change{Path: event.Path.Clone(), Tag: replacement.Tag(), Hook: registered.registration.Name, Kind: ChangeTransformed})
		}
		if decision.Filter {
			result.Filter = true
			tag := eventTag(event)
			result.Changes = append(result.Changes, Change{Path: event.Path.Clone(), Tag: tag, Hook: registered.registration.Name, Kind: ChangeFiltered})
		}
		if decision.SkipValue {
			result.SkipValue = true
			tag := eventTag(event)
			result.Changes = append(result.Changes, Change{Path: event.Path.Clone(), Tag: tag, Hook: registered.registration.Name, Kind: ChangeSkipped})
		}
		if decision.DeferValue {
			result.DeferValue = true
			tag := eventTag(event)
			result.Changes = append(result.Changes, Change{Path: event.Path.Clone(), Tag: tag, Hook: registered.registration.Name, Kind: ChangeDeferred})
		}
		if stopAfterFinding && len(decision.Findings) > 0 {
			break
		}
	}
	return result, nil
}

type HookError struct {
	Hook string
	Code Code
	Err  error
}

func (e *HookError) Error() string {
	return fmt.Sprintf("dicom validation hook %q failed (%s)", e.Hook, e.Code)
}
func (e *HookError) Unwrap() error { return ErrHookFailed }

func (e *HookError) Is(target error) bool {
	return target == ErrHookFailed || (target == ErrHookAction && e.Err == ErrHookAction)
}

func (c *HookChain) hookFailure(result HookResult, registered *registeredHook, event HookEvent, code Code, cause error) (HookResult, error) {
	if registered.registration.OnFailure == HookFailureReject {
		return result, &HookError{Hook: registered.registration.Name, Code: code, Err: cause}
	}
	finding := Finding{
		Path: event.Path.Clone(), Tag: eventTag(event), Severity: SeverityWarning,
		Code: code, Offset: event.Offset, OffsetSet: event.OffsetSet,
		Hook: registered.registration.Name, Message: "hook failed without applying its decision",
	}
	if event.Element != nil {
		finding.VR = event.Element.VR()
	} else if event.Header != nil {
		finding.VR = event.Header.VR
	}
	result.Findings = append(result.Findings, finding)
	return result, nil
}

func invokeHook(parent context.Context, registered *registeredHook, event HookEvent) (decision HookDecision, code Code, err error) {
	if err := parent.Err(); err != nil {
		return HookDecision{}, CodeHookTimeout, err
	}
	defer func() {
		if recover() != nil {
			decision = HookDecision{}
			code = CodeHookPanic
			err = ErrHookFailed
		}
	}()
	decision, err = registered.registration.Hook.HandleValidationHook(parent, cloneHookEvent(event))
	if parent.Err() != nil {
		return HookDecision{}, CodeHookTimeout, parent.Err()
	}
	if err != nil {
		return HookDecision{}, CodeHookError, err
	}
	return decision, "", nil
}

func acquireHook(ctx context.Context, registered *registeredHook) error {
	if registered.gate == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-registered.gate:
		return nil
	}
}

func releaseHook(registered *registeredHook) {
	if registered.gate != nil {
		registered.gate <- struct{}{}
	}
}

func validHookPoint(point HookPoint) bool {
	switch point {
	case HookElementHeaderRead, HookAfterElement, HookItemComplete, HookSequenceComplete,
		HookDataSetComplete, HookPreValidation, HookPostValidation, HookPreSerialization, HookPostWrite:
		return true
	default:
		return false
	}
}

func validHookDecision(point HookPoint, decision HookDecision) bool {
	if decision.SkipValue && decision.DeferValue {
		return false
	}
	if decision.Element != nil && decision.Filter {
		return false
	}
	switch point {
	case HookElementHeaderRead:
		return decision.Element == nil && !decision.Filter
	case HookAfterElement, HookSequenceComplete, HookPreSerialization:
		return !decision.SkipValue && !decision.DeferValue
	case HookItemComplete, HookDataSetComplete, HookPreValidation, HookPostValidation, HookPostWrite:
		return decision.Element == nil && !decision.Filter && !decision.SkipValue && !decision.DeferValue
	default:
		return false
	}
}

func eventTag(event HookEvent) core.Tag {
	if event.Element != nil {
		return event.Element.Tag()
	}
	if event.Header != nil {
		return event.Header.Tag
	}
	if len(event.Path) > 0 {
		return event.Path[len(event.Path)-1].Tag
	}
	return core.Tag{}
}

func cloneHookEvent(event HookEvent) HookEvent {
	clone := event
	clone.Path = event.Path.Clone()
	if event.Header != nil {
		header := *event.Header
		clone.Header = &header
	}
	clone.Element = cloneElementPtr(event.Element)
	if event.DataSet != nil {
		dataset := cloneDataSet(*event.DataSet)
		clone.DataSet = &dataset
	}
	return clone
}

func cloneElementPtr(element *core.Element) *core.Element {
	if element == nil {
		return nil
	}
	clone := cloneElement(*element)
	return &clone
}
