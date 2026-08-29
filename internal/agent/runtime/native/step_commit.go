package native

import (
	"reflect"
	"sync"

	sdk "github.com/felinics/twilight/sdk"
)

// stepMessageCapture retains context messages appended by PrepareStep. They
// are input to the next model call, so they become durable only with that
// call's complete step.
type stepMessageCapture struct {
	mu          sync.Mutex
	byStep      map[int][]sdk.Message
	prepared    map[int]preparedMessageCapture
	nextStep    int
	lastStep    int
	onReconcile []func(int, []admittedPreparedMessage)
}

type preparedMessageSpan struct {
	start int
	end   int
}

type preparedMessageCapture struct {
	messages []sdk.Message
	span     preparedMessageSpan
}

type admittedPreparedMessage struct {
	index int
}

// preparedMessageProvenance maps provider messages back to the raw
// PrepareStep capture whose dynamic additions may become durable. Indexes are
// absolute in preparedMessageCapture.messages; -1 marks transient or
// synthetic messages. The sidecar is private so provider payloads remain
// unchanged.
type preparedMessageProvenance struct {
	step           int
	messageIndexes []int
	known          bool
}

func (c *stepMessageCapture) messages(stepIndex int) []sdk.Message {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneProviderMessages(c.byStep[stepIndex])
}

func capturePreparedStepMessages(prepare func(*sdk.GenerateParams) *sdk.GenerateParams) (func(*sdk.GenerateParams) *sdk.GenerateParams, *stepMessageCapture) {
	capture := &stepMessageCapture{
		byStep:   make(map[int][]sdk.Message),
		prepared: make(map[int]preparedMessageCapture),
		nextStep: 1, // Twilight calls PrepareStep only before steps after step zero.
	}
	if prepare == nil {
		return nil, capture
	}
	return func(params *sdk.GenerateParams) *sdk.GenerateParams {
		before := len(params.Messages)
		override := prepare(params)
		actual := params
		if override != nil {
			actual = override
		}
		capture.mu.Lock()
		step := capture.nextStep
		capture.nextStep++
		capture.lastStep = step
		delete(capture.byStep, step)
		delete(capture.prepared, step)
		if actual != nil && len(actual.Messages) > before {
			capture.byStep[step] = cloneProviderMessages(actual.Messages[before:])
			capture.prepared[step] = preparedMessageCapture{
				messages: cloneProviderMessages(actual.Messages),
				span:     preparedMessageSpan{start: before, end: len(actual.Messages)},
			}
		}
		capture.mu.Unlock()
		return override
	}, capture
}

func (c *stepMessageCapture) addAdmissionObserver(observer func(int, []admittedPreparedMessage)) {
	if c == nil || observer == nil {
		return
	}
	c.mu.Lock()
	c.onReconcile = append(c.onReconcile, observer)
	c.mu.Unlock()
}

func (c *stepMessageCapture) latestProvenance(messages []sdk.Message) preparedMessageProvenance {
	if c == nil {
		return preparedMessageProvenance{}
	}
	c.mu.Lock()
	step := c.lastStep
	captured, ok := c.prepared[step]
	c.mu.Unlock()
	if !ok {
		return preparedMessageProvenance{}
	}

	provenance := preparedMessageProvenance{step: step, known: true}
	if len(messages) < len(captured.messages) ||
		!providerMessagesEqual(captured.messages, messages[:len(captured.messages)]) {
		return provenance
	}
	provenance.messageIndexes = make([]int, len(messages))
	for i := range provenance.messageIndexes {
		provenance.messageIndexes[i] = -1
	}
	for i := range captured.messages {
		provenance.messageIndexes[i] = i
	}
	return provenance
}

// reconcile makes one dispatched provider attempt authoritative for the raw
// PrepareStep additions it actually retained. Hook, background, and synthetic
// carriers have source index -1 and remain transient. A retry can revise the
// same capture by publishing a new exact provenance sidecar.
func (c *stepMessageCapture) reconcile(admitted []sdk.Message, provenance preparedMessageProvenance) {
	if c == nil || !provenance.known || len(provenance.messageIndexes) != len(admitted) {
		return
	}
	c.mu.Lock()
	captured, ok := c.prepared[provenance.step]
	observers := append([]func(int, []admittedPreparedMessage){}, c.onReconcile...)
	c.mu.Unlock()
	if !ok {
		return
	}

	retained, admissions := retainedPreparedMessages(captured, admitted, provenance.messageIndexes)
	c.mu.Lock()
	if len(retained) == 0 {
		delete(c.byStep, provenance.step)
	} else {
		c.byStep[provenance.step] = cloneProviderMessages(retained)
	}
	c.mu.Unlock()
	for _, observer := range observers {
		observer(provenance.step, clonePreparedAdmissions(admissions))
	}
}

// revoke removes provider-visibility admission for an attempt that will not
// be dispatched. Unlike reconcile, revocation does not require an aligned
// provider message vector: the step identity alone is authoritative.
func (c *stepMessageCapture) revoke(provenance preparedMessageProvenance) {
	if c == nil || !provenance.known {
		return
	}
	c.mu.Lock()
	delete(c.byStep, provenance.step)
	observers := append([]func(int, []admittedPreparedMessage){}, c.onReconcile...)
	c.mu.Unlock()
	for _, observer := range observers {
		observer(provenance.step, nil)
	}
}

func retainedPreparedMessages(captured preparedMessageCapture, admitted []sdk.Message, sourceIndexes []int) ([]sdk.Message, []admittedPreparedMessage) {
	if captured.span.start < 0 || captured.span.end > len(captured.messages) ||
		captured.span.start >= captured.span.end || len(sourceIndexes) != len(admitted) {
		return nil, nil
	}
	preparedComparable := cloneProviderMessages(captured.messages)
	admittedComparable := cloneProviderMessages(admitted)
	clearProviderCacheControls(preparedComparable)
	clearProviderCacheControls(admittedComparable)

	retained := make([]bool, len(preparedComparable))
	for i, sourceIndex := range sourceIndexes {
		if sourceIndex < captured.span.start || sourceIndex >= captured.span.end ||
			sourceIndex >= len(preparedComparable) {
			continue
		}
		if reflect.DeepEqual(preparedComparable[sourceIndex], admittedComparable[i]) {
			retained[sourceIndex] = true
		}
	}

	out := make([]sdk.Message, 0, captured.span.end-captured.span.start)
	admissions := make([]admittedPreparedMessage, 0, captured.span.end-captured.span.start)
	for i := captured.span.start; i < captured.span.end; i++ {
		if retained[i] {
			out = append(out, captured.messages[i])
			admissions = append(admissions, admittedPreparedMessage{index: i})
		}
	}
	return out, admissions
}

func clonePreparedMessageProvenance(provenance preparedMessageProvenance) preparedMessageProvenance {
	provenance.messageIndexes = append([]int(nil), provenance.messageIndexes...)
	return provenance
}

func (p preparedMessageProvenance) aligned(messageCount int) bool {
	return !p.known || len(p.messageIndexes) == messageCount
}

func (p preparedMessageProvenance) prependSynthetic() preparedMessageProvenance {
	if !p.known {
		return p
	}
	p.messageIndexes = append([]int{-1}, p.messageIndexes...)
	return p
}

func composePreparedMessageProvenance(
	input preparedMessageProvenance,
	inputMessageCount int,
	sourceIndexes []int,
) (preparedMessageProvenance, bool) {
	if !input.aligned(inputMessageCount) {
		return preparedMessageProvenance{}, false
	}
	if !input.known {
		return preparedMessageProvenance{}, true
	}
	composed := preparedMessageProvenance{
		step:           input.step,
		messageIndexes: make([]int, len(sourceIndexes)),
		known:          true,
	}
	for i, sourceIndex := range sourceIndexes {
		if sourceIndex < 0 {
			composed.messageIndexes[i] = -1
			continue
		}
		if sourceIndex >= len(input.messageIndexes) {
			return preparedMessageProvenance{}, false
		}
		composed.messageIndexes[i] = input.messageIndexes[sourceIndex]
	}
	return composed, true
}

func failClosedPreparedMessageProvenance(
	input preparedMessageProvenance,
	outputMessageCount,
	prefixCount int,
) preparedMessageProvenance {
	if !input.known {
		return input
	}
	output := preparedMessageProvenance{
		step:           input.step,
		messageIndexes: make([]int, outputMessageCount),
		known:          true,
	}
	for i := range output.messageIndexes {
		output.messageIndexes[i] = -1
	}
	if prefixCount > outputMessageCount {
		prefixCount = outputMessageCount
	}
	if prefixCount > len(input.messageIndexes) {
		return output
	}
	copy(output.messageIndexes[:prefixCount], input.messageIndexes[:prefixCount])
	return output
}

func selectionMessageSourceIndexes(
	before []sdk.Message,
	after []sdk.Message,
	prefixCount int,
	selection ContextStepSelectionResult,
) ([]int, bool) {
	beforeComparable := cloneProviderMessages(before)
	afterComparable := cloneProviderMessages(after)
	clearProviderCacheControls(beforeComparable)
	clearProviderCacheControls(afterComparable)
	if prefixCount < 0 || len(beforeComparable) < prefixCount || len(afterComparable) < prefixCount ||
		!reflect.DeepEqual(beforeComparable[:prefixCount], afterComparable[:prefixCount]) {
		return nil, false
	}

	fallback := make([]int, len(afterComparable))
	for i := range fallback {
		fallback[i] = -1
	}
	for i := 0; i < prefixCount; i++ {
		fallback[i] = i
	}

	if selection.MessageSourceIndexesKnown {
		if len(selection.MessageSourceIndexes) != len(after) {
			return fallback, true
		}
		lastSource := -1
		for i, sourceIndex := range selection.MessageSourceIndexes {
			if sourceIndex < -1 || sourceIndex >= len(before) {
				return fallback, true
			}
			if i < prefixCount && sourceIndex != i {
				return fallback, true
			}
			if sourceIndex >= 0 {
				if sourceIndex <= lastSource {
					return fallback, true
				}
				if !providerMessagesEqual(before[sourceIndex:sourceIndex+1], after[i:i+1]) {
					return fallback, true
				}
				lastSource = sourceIndex
			}
		}
		return append([]int(nil), selection.MessageSourceIndexes...), true
	}

	if len(beforeComparable) == len(afterComparable) && reflect.DeepEqual(beforeComparable, afterComparable) {
		for i := range fallback {
			fallback[i] = i
		}
	}
	return fallback, true
}

func providerMessagesEqual(left, right []sdk.Message) bool {
	leftComparable := cloneProviderMessages(left)
	rightComparable := cloneProviderMessages(right)
	clearProviderCacheControls(leftComparable)
	clearProviderCacheControls(rightComparable)
	return reflect.DeepEqual(leftComparable, rightComparable)
}

func clonePreparedAdmissions(admissions []admittedPreparedMessage) []admittedPreparedMessage {
	cloned := make([]admittedPreparedMessage, len(admissions))
	copy(cloned, admissions)
	return cloned
}

func (c *stepMessageCapture) decorate(stepIndex int, step *sdk.StepResult, metadata *toolExecutionMetadataRegistry) *sdk.StepResult {
	if step == nil {
		return nil
	}
	decorated := *step
	decorated.Messages = append(c.messages(stepIndex), step.Messages...)
	if decorated.DeferredToolApproval != nil {
		decorated.Messages = annotateDeferredApproval(decorated.Messages, *decorated.DeferredToolApproval)
	}
	decorated.Messages = metadata.annotate(decorated.Messages)
	return &decorated
}
