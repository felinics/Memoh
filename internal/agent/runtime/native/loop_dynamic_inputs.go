package native

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
)

// loopDynamicInputs owns the user messages the step loop itself appends to the
// thread at step boundaries: live InjectCh messages and read-media carriers.
// The loop appends them before a step's prepare chain runs, so it knows each
// message's exact position in the pre-selection provider input. Whether the
// dispatched provider attempt actually carried a message (its admission) is
// decided at the publish boundary and drives committed-step decoration,
// terminal read-media merging, and the legacy injected recorder.
type loopDynamicInputs struct {
	mu      sync.Mutex
	records []dynamicInputRecord
	// offset is the durable step index of this loop's first step
	// (RunConfig.StepIndexOffset). Record boundaries and decoration lookups
	// are durable indexes so continuation segments (offset > 0) persist
	// dynamic inputs against the same key OnStepCommitted uses.
	offset int
	// activeBoundary is the durable step index the next dispatch targets.
	// The loop advances it at each drain point; a mid-stream retry
	// re-dispatches the same boundary.
	activeBoundary int
}

func newLoopDynamicInputs(offset int) *loopDynamicInputs {
	if offset < 0 {
		offset = 0
	}
	return &loopDynamicInputs{offset: offset}
}

type dynamicInputRecord struct {
	// boundary is the segment-local committed-step count when the message was
	// appended: the message is input to durable step `boundary`.
	boundary  int
	message   sdk.Message
	readMedia bool
	// text is the recorder text for injected messages; empty for read-media.
	text     string
	admitted bool
	// sourceIndex is the message's position in the thread at append time,
	// which is also its position in the next dispatch's pre-selection input.
	sourceIndex int
}

// dynamicSourceRef names one tracked record and its index in a concrete
// provider message slice: the pre-selection params of the next dispatch, or a
// stored provider attempt's retry input.
type dynamicSourceRef struct {
	recordID int
	index    int
}

// beginBoundary marks the durable boundary the next dispatch targets. Called
// by the loop at every drain point, whether or not anything is drained, so a
// later reject can never revoke an earlier boundary's records. segmentLocal
// is the committed-step count of this loop invocation; the stored boundary
// is offset plus that count.
func (d *loopDynamicInputs) beginBoundary(segmentLocal int) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.activeBoundary = d.offset + segmentLocal
	d.mu.Unlock()
}

// append records one loop-appended dynamic message at the active durable
// boundary and its append position.
func (d *loopDynamicInputs) append(message sdk.Message, readMedia bool, text string, sourceIndex int) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.records = append(d.records, dynamicInputRecord{
		boundary:    d.activeBoundary,
		message:     message,
		readMedia:   readMedia,
		text:        text,
		sourceIndex: sourceIndex,
	})
	d.mu.Unlock()
}

// pendingRefs returns the active boundary's records with their append-time
// positions, valid for the pre-selection input of the next in-loop dispatch.
func (d *loopDynamicInputs) pendingRefs() []dynamicSourceRef {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []dynamicSourceRef
	for id := range d.records {
		if d.records[id].boundary == d.activeBoundary {
			out = append(out, dynamicSourceRef{recordID: id, index: d.records[id].sourceIndex})
		}
	}
	return out
}

// recordMessage returns the tracked message for one record.
func (d *loopDynamicInputs) recordMessage(recordID int) (sdk.Message, bool) {
	if d == nil {
		return sdk.Message{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if recordID < 0 || recordID >= len(d.records) {
		return sdk.Message{}, false
	}
	return d.records[recordID].message, true
}

// commit makes one dispatched provider attempt authoritative for the active
// boundary: exactly the listed records are admitted, every other record at the
// boundary is not. A mid-stream retry re-commits the same boundary with the
// refs its own published payload retained.
func (d *loopDynamicInputs) commit(admitted []dynamicSourceRef) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.records {
		if d.records[i].boundary == d.activeBoundary {
			d.records[i].admitted = false
		}
	}
	for _, ref := range admitted {
		if ref.recordID >= 0 && ref.recordID < len(d.records) && d.records[ref.recordID].boundary == d.activeBoundary {
			d.records[ref.recordID].admitted = true
		}
	}
}

// revoke removes admission for the active boundary: the attempt that would
// have carried these messages will not be dispatched.
func (d *loopDynamicInputs) revoke() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.records {
		if d.records[i].boundary == d.activeBoundary {
			d.records[i].admitted = false
		}
	}
}

// stepAdditions returns the admitted dynamic messages that were input to the
// committed step at stepIndex, in append order, for step decoration. Both
// the lookup key and the record boundary are the durable step index
// (offset plus segment-local step count).
func (d *loopDynamicInputs) stepAdditions(stepIndex int) []sdk.Message {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []sdk.Message
	for i := range d.records {
		if d.records[i].boundary == stepIndex && d.records[i].admitted {
			out = append(out, d.records[i].message)
		}
	}
	return cloneProviderMessages(out)
}

// readMediaDurable reports whether one read-media record reaches the terminal
// message set: admitted, its target step committed, and that step is not the
// separately persisted interrupted checkpoint (which is decorated with the
// same admitted input).
func (r dynamicInputRecord) readMediaDurable(completedStepCount, interruptedDurableStep int) bool {
	return r.readMedia && r.admitted && r.boundary >= 0 &&
		r.boundary < completedStepCount && r.boundary != interruptedDurableStep
}

// mergeReadMedia interleaves durable read-media carriers into the terminal
// message set at the boundary each was injected: after the outputs of the step
// preceding its target step. The second result names the carriers' positions
// in the merged set: internal feedback the model produced for itself, which
// persistence files under the current user turn rather than as a new one.
func (d *loopDynamicInputs) mergeReadMedia(steps []step.Record, fallback []sdk.Message, interruptedDurableStep int) ([]sdk.Message, []int) {
	if d == nil {
		return fallback, nil
	}
	d.mu.Lock()
	records := append([]dynamicInputRecord(nil), d.records...)
	d.mu.Unlock()
	hasReadMedia := false
	for _, record := range records {
		if record.readMedia {
			hasReadMedia = true
			break
		}
	}
	if !hasReadMedia || len(steps) == 0 {
		return fallback, nil
	}

	var feedbackIndexes []int
	completed := d.offset + len(steps)
	merged := make([]sdk.Message, 0, len(fallback)+len(records))
	for stepIndex, step := range steps {
		merged = append(merged, step.Messages...)
		target := d.offset + stepIndex + 1
		for _, record := range records {
			if record.boundary == target && record.readMediaDurable(completed, interruptedDurableStep) {
				feedbackIndexes = append(feedbackIndexes, len(merged))
				merged = append(merged, record.message)
			}
		}
	}
	return merged, feedbackIndexes
}

// flushInjected drives the legacy append-only injected-message recorder:
// only messages admitted by a dispatched provider attempt whose target step
// completed are recorded, positioned by the step outputs that preceded them
// plus any durable read-media carriers at or before the same boundary.
func (d *loopDynamicInputs) flushInjected(steps []step.Record, interruptedDurableStep int, recorder func(headerifiedText string, insertAfter int)) {
	if d == nil || recorder == nil {
		return
	}
	d.mu.Lock()
	records := append([]dynamicInputRecord(nil), d.records...)
	d.mu.Unlock()

	outputCounts := make([]int, len(steps)+1)
	for i := range steps {
		outputCounts[i+1] = outputCounts[i] + len(steps[i].Messages)
	}
	completed := d.offset + len(steps)
	for _, record := range records {
		local := record.boundary - d.offset
		if record.readMedia || !record.admitted || local < 0 || local >= len(steps) {
			continue
		}
		insertAfter := outputCounts[local]
		for _, media := range records {
			if media.readMediaDurable(completed, interruptedDurableStep) && media.boundary <= record.boundary {
				insertAfter++
			}
		}
		recorder(record.text, insertAfter)
	}
}

// decorateCommittedStep prepends the admitted loop-appended input messages to
// one completed step so persistence sees the input exactly as the provider
// did, then applies the deferred-approval and execution-metadata annotations.
func decorateCommittedStep(additions []sdk.Message, record *step.Record, metadata *toolExecutionMetadataRegistry) *step.Record {
	if record == nil {
		return nil
	}
	decorated := *record
	messages := make([]sdk.Message, 0, len(additions)+len(record.Messages))
	messages = append(messages, additions...)
	messages = append(messages, record.Messages...)
	decorated.Messages = messages
	if decorated.Deferred != nil {
		decorated.Messages = annotateDeferredApproval(decorated.Messages, *decorated.Deferred)
	}
	decorated.Messages = metadata.annotate(decorated.Messages)
	return &decorated
}

// verifyDynamicRefs drops refs that do not verifiably point at their tracked
// message in the given provider input; a message that cannot be located is
// treated as not carried.
func verifyDynamicRefs(dynamic *loopDynamicInputs, refs []dynamicSourceRef, messages []sdk.Message) []dynamicSourceRef {
	if len(refs) == 0 || dynamic == nil {
		return nil
	}
	out := make([]dynamicSourceRef, 0, len(refs))
	for _, ref := range refs {
		if ref.index < 0 || ref.index >= len(messages) {
			continue
		}
		message, ok := dynamic.recordMessage(ref.recordID)
		if !ok || !providerMessagesEqual([]sdk.Message{message}, messages[ref.index:ref.index+1]) {
			continue
		}
		out = append(out, ref)
	}
	return out
}

// remapDynamicRefs maps dynamic refs through one applied step reselection.
// A selection with a verified source-index vector relocates each surviving
// ref; an unchanged selection keeps them; any unverifiable change fails
// closed and drops every ref, so a message the published payload cannot be
// proven to carry is never treated as durable.
func remapDynamicRefs(refs []dynamicSourceRef, before, after []sdk.Message, prefixCount int, selection ContextStepSelectionResult) []dynamicSourceRef {
	if len(refs) == 0 {
		return nil
	}
	if selection.MessageSourceIndexesKnown {
		if !validSelectionSourceIndexes(before, after, prefixCount, selection.MessageSourceIndexes) {
			return nil
		}
		bySource := make(map[int]int, len(selection.MessageSourceIndexes))
		for i, sourceIndex := range selection.MessageSourceIndexes {
			if sourceIndex >= 0 {
				bySource[sourceIndex] = i
			}
		}
		out := make([]dynamicSourceRef, 0, len(refs))
		for _, ref := range refs {
			if index, ok := bySource[ref.index]; ok {
				out = append(out, dynamicSourceRef{recordID: ref.recordID, index: index})
			}
		}
		return out
	}
	if providerMessagesEqual(before, after) {
		return refs
	}
	return nil
}

// validSelectionSourceIndexes verifies a reselector-reported source-index
// vector: aligned with the output, prefix identity preserved, sources
// strictly increasing, and every claimed origin content-equal to its output.
func validSelectionSourceIndexes(before, after []sdk.Message, prefixCount int, sourceIndexes []int) bool {
	if len(sourceIndexes) != len(after) {
		return false
	}
	lastSource := -1
	for i, sourceIndex := range sourceIndexes {
		if sourceIndex < -1 || sourceIndex >= len(before) {
			return false
		}
		if i < prefixCount && sourceIndex != i {
			return false
		}
		if sourceIndex >= 0 {
			if sourceIndex <= lastSource {
				return false
			}
			if !providerMessagesEqual(before[sourceIndex:sourceIndex+1], after[i:i+1]) {
				return false
			}
			lastSource = sourceIndex
		}
	}
	return true
}

func providerMessagesEqual(left, right []sdk.Message) bool {
	leftComparable := cloneProviderMessages(left)
	rightComparable := cloneProviderMessages(right)
	clearProviderCacheControls(leftComparable)
	clearProviderCacheControls(rightComparable)
	return reflect.DeepEqual(leftComparable, rightComparable)
}

func collectDirectiveInputs(dst []DirectiveInput, src []DirectiveInput) []DirectiveInput {
	for _, input := range src {
		if strings.TrimSpace(input.Text) == "" {
			continue
		}
		dst = append(dst, input)
	}
	return dst
}

func appendDirectiveInputs(cfg RunConfig, dynamic *loopDynamicInputs, messages []sdk.Message, inputs []DirectiveInput) []sdk.Message {
	for _, input := range inputs {
		text := strings.TrimSpace(input.Text)
		if text == "" {
			continue
		}
		message := sdk.UserMessage(text)
		if cfg.ContextMutations != nil {
			cfg.ContextMutations.Record(contextfrag.MutationInjectedMessage, fmt.Sprintf("bytes=%d", len(text)))
		}
		dynamic.append(message, false, text, len(messages))
		messages = append(messages, message)
	}
	return messages
}

func cloneDynamicSourceRefs(refs []dynamicSourceRef) []dynamicSourceRef {
	if refs == nil {
		return nil
	}
	return append([]dynamicSourceRef(nil), refs...)
}

// shiftDynamicSourceRefs offsets every ref index, dropping refs shifted out of
// range (a record can never be the promoted system message).
func shiftDynamicSourceRefs(refs []dynamicSourceRef, delta int) []dynamicSourceRef {
	if len(refs) == 0 {
		return refs
	}
	out := make([]dynamicSourceRef, 0, len(refs))
	for _, ref := range refs {
		ref.index += delta
		if ref.index < 0 {
			continue
		}
		out = append(out, ref)
	}
	return out
}
