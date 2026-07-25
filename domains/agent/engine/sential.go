package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Loop detection constants matching the TypeScript implementation.
const (
	LoopDetectedAbortMessage        = "loop detected, stream aborted"
	LoopDetectedStreakThreshold     = 3
	LoopDetectedMinNewGramsPerChunk = 8
	LoopDetectedProbeChars          = 256
	ToolLoopDetectedAbortMessage    = "tool loop detected, stream aborted"
	ToolLoopRepeatThreshold         = 5
	ToolLoopWarningsBeforeAbort     = 1
	ToolLoopWarningKey              = "__memoh_tool_loop_warning"                                                                                                                                                                            //nolint:gosec // internal warning key, not a credential
	ToolLoopWarningText             = "[MEMOH_TOOL_LOOP_WARNING] Repeated identical tool invocation (same tool + arguments) was detected more than 5 times. Stop looping this tool and either summarize current results or change strategy." //nolint:gosec // human-readable warning text, not a credential

	defaultNgramSize           = 10
	defaultWindowSize          = 1000
	defaultOverlapThreshold    = 0.75
	defaultConsecutiveHits     = 10
	defaultMinNewGramsPerChunk = 1
)

var (
	ErrTextLoopDetected = errors.New(LoopDetectedAbortMessage)
	ErrToolLoopDetected = errors.New(ToolLoopDetectedAbortMessage)
)

// --- Sential: n-gram overlap detector ---

// SentialOptions configures the n-gram overlap detector.
type SentialOptions struct {
	NgramSize        int
	WindowSize       int
	OverlapThreshold float64
}

// SentialResult is the output of an overlap inspection.
type SentialResult struct {
	Hit          bool
	Overlap      float64
	MatchedGrams int
	NewGrams     int
}

// Sential detects text repetition via n-gram overlap.
type Sential struct {
	ngramSize        int
	windowSize       int
	overlapThreshold float64
	windowChars      []rune
	windowNgramQueue []string
	historySet       map[string]struct{}
	historyCounts    map[string]int
}

// NewSential creates a new n-gram overlap detector.
func NewSential(opts SentialOptions) *Sential {
	ngramSize := opts.NgramSize
	if ngramSize <= 0 {
		ngramSize = defaultNgramSize
	}
	windowSize := opts.WindowSize
	if windowSize <= 0 {
		windowSize = defaultWindowSize
	}
	overlapThreshold := opts.OverlapThreshold
	if overlapThreshold <= 0 {
		overlapThreshold = defaultOverlapThreshold
	}
	return &Sential{
		ngramSize:        ngramSize,
		windowSize:       windowSize,
		overlapThreshold: overlapThreshold,
		historySet:       make(map[string]struct{}),
		historyCounts:    make(map[string]int),
	}
}

func (s *Sential) addHistoryGram(gram string) {
	s.historyCounts[gram]++
	if s.historyCounts[gram] == 1 {
		s.historySet[gram] = struct{}{}
	}
}

func (s *Sential) removeHistoryGram(gram string) {
	count := s.historyCounts[gram]
	if count <= 1 {
		delete(s.historyCounts, gram)
		delete(s.historySet, gram)
		return
	}
	s.historyCounts[gram] = count - 1
}

func (s *Sential) pushWindowChar(ch rune) {
	s.windowChars = append(s.windowChars, ch)
	if len(s.windowChars) >= s.ngramSize {
		start := len(s.windowChars) - s.ngramSize
		gram := string(s.windowChars[start : start+s.ngramSize])
		s.windowNgramQueue = append(s.windowNgramQueue, gram)
		s.addHistoryGram(gram)
	}
	if len(s.windowChars) <= s.windowSize {
		return
	}
	s.windowChars = s.windowChars[1:]
	if len(s.windowNgramQueue) > 0 {
		removed := s.windowNgramQueue[0]
		s.windowNgramQueue = s.windowNgramQueue[1:]
		s.removeHistoryGram(removed)
	}
}

// Inspect checks a chunk of text for n-gram overlap with the sliding window.
func (s *Sential) Inspect(text string) SentialResult {
	incoming := []rune(text)
	if len(incoming) == 0 {
		return SentialResult{}
	}

	contextSize := s.ngramSize - 1
	if contextSize < 0 {
		contextSize = 0
	}
	var contextChars []rune
	if contextSize > 0 && len(s.windowChars) > 0 {
		start := len(s.windowChars) - contextSize
		if start < 0 {
			start = 0
		}
		contextChars = make([]rune, len(s.windowChars[start:]))
		copy(contextChars, s.windowChars[start:])
	}
	candidate := append([]rune{}, contextChars...)
	candidate = append(candidate, incoming...)
	contextLength := len(contextChars)

	matchedGrams := 0
	newGrams := 0
	if len(candidate) >= s.ngramSize {
		for i := 0; i <= len(candidate)-s.ngramSize; i++ {
			gramEndIndex := i + s.ngramSize - 1
			if gramEndIndex < contextLength {
				continue
			}
			gram := string(candidate[i : i+s.ngramSize])
			newGrams++
			if _, ok := s.historySet[gram]; ok {
				matchedGrams++
			}
		}
	}

	overlap := 0.0
	if newGrams > 0 {
		overlap = float64(matchedGrams) / float64(newGrams)
	}
	hit := overlap > s.overlapThreshold

	for _, ch := range incoming {
		s.pushWindowChar(ch)
	}

	return SentialResult{
		Hit:          hit,
		Overlap:      overlap,
		MatchedGrams: matchedGrams,
		NewGrams:     newGrams,
	}
}

// Reset clears the detector state.
func (s *Sential) Reset() {
	s.windowChars = nil
	s.windowNgramQueue = nil
	s.historySet = make(map[string]struct{})
	s.historyCounts = make(map[string]int)
}

// --- Text Loop Guard ---

// TextLoopGuardResult extends SentialResult with streak and abort tracking.
type TextLoopGuardResult struct {
	SentialResult
	Streak int
	Abort  bool
}

// TextLoopGuard wraps Sential with consecutive-hit tracking.
type TextLoopGuard struct {
	sential                *Sential
	consecutiveHitsToAbort int
	minNewGramsPerChunk    int
	streak                 int
}

// NewTextLoopGuard creates a text loop guard.
func NewTextLoopGuard(consecutiveHits, minNewGrams int, opts SentialOptions) *TextLoopGuard {
	if consecutiveHits <= 0 {
		consecutiveHits = defaultConsecutiveHits
	}
	if minNewGrams <= 0 {
		minNewGrams = defaultMinNewGramsPerChunk
	}
	return &TextLoopGuard{
		sential:                NewSential(opts),
		consecutiveHitsToAbort: consecutiveHits,
		minNewGramsPerChunk:    minNewGrams,
	}
}

// Inspect checks text and tracks consecutive overlap streaks.
func (g *TextLoopGuard) Inspect(text string) TextLoopGuardResult {
	result := g.sential.Inspect(text)
	if result.NewGrams >= g.minNewGramsPerChunk {
		if result.Hit {
			g.streak++
		} else {
			g.streak = 0
		}
	}
	return TextLoopGuardResult{
		SentialResult: result,
		Streak:        g.streak,
		Abort:         g.streak >= g.consecutiveHitsToAbort,
	}
}

// Reset clears the guard state.
func (g *TextLoopGuard) Reset() {
	g.sential.Reset()
	g.streak = 0
}

// --- Text Loop Probe Buffer ---

// TextLoopProbeBuffer batches text into chunks before passing to an inspector.
type TextLoopProbeBuffer struct {
	chunkSize int
	inspect   func(string)
	chars     []rune
	offset    int
}

// NewTextLoopProbeBuffer creates a probe buffer.
func NewTextLoopProbeBuffer(chunkSize int, inspect func(string)) *TextLoopProbeBuffer {
	if chunkSize <= 0 {
		chunkSize = LoopDetectedProbeChars
	}
	return &TextLoopProbeBuffer{
		chunkSize: chunkSize,
		inspect:   inspect,
	}
}

// Push adds text to the buffer, emitting full chunks to the inspector.
func (b *TextLoopProbeBuffer) Push(text string) {
	if text == "" {
		return
	}
	b.chars = append(b.chars, []rune(text)...)
	for len(b.chars)-b.offset >= b.chunkSize {
		chunk := string(b.chars[b.offset : b.offset+b.chunkSize])
		b.offset += b.chunkSize
		if len(chunk) > 0 {
			b.inspect(chunk)
		}
	}
	if b.offset >= b.chunkSize {
		b.chars = b.chars[b.offset:]
		b.offset = 0
	}
}

// Flush emits any remaining content to the inspector.
func (b *TextLoopProbeBuffer) Flush() {
	if len(b.chars)-b.offset > 0 {
		remainder := string(b.chars[b.offset:])
		if len(remainder) > 0 {
			b.inspect(remainder)
		}
	}
	b.chars = nil
	b.offset = 0
}

// --- Tool Loop Guard ---

var defaultVolatileKeys = []string{
	"toolcallid", "toolcallid", "requestid", "requestid",
	"traceid", "traceid", "spanid", "spanid",
	"sessionid", "sessionid", "timestamp",
	"createdat", "createdat", "updatedat", "updatedat",
	"expiresat", "expiresat", "nonce",
}

var volatileKeySuffixes = []string{
	"requestid", "traceid", "sessionid", "toolcallid",
	"timestamp", "createdat", "updatedat", "expiresat",
}

// ToolLoopInput represents a tool call for loop detection.
type ToolLoopInput struct {
	ToolName string
	Input    any
}

// ToolLoopResult is the output of a tool loop inspection.
type ToolLoopResult struct {
	Hash        string
	RepeatCount int
	BreachCount int
	Warn        bool
	Abort       bool
}

// ToolLoopGuard detects repeated identical tool calls.
type ToolLoopGuard struct {
	mu                  sync.Mutex
	repeatThreshold     int
	warningsBeforeAbort int
	volatileKeySet      map[string]struct{}
	lastHash            string
	repeatCount         int
	breachCount         int
	breachHash          string
}

// NewToolLoopGuard creates a tool loop guard.
func NewToolLoopGuard(repeatThreshold, warningsBeforeAbort int) *ToolLoopGuard {
	if repeatThreshold <= 0 {
		repeatThreshold = ToolLoopRepeatThreshold
	}
	if warningsBeforeAbort <= 0 {
		warningsBeforeAbort = ToolLoopWarningsBeforeAbort
	}
	volatileSet := make(map[string]struct{})
	for _, k := range defaultVolatileKeys {
		volatileSet[normalizeKeyName(k)] = struct{}{}
	}
	return &ToolLoopGuard{
		repeatThreshold:     repeatThreshold,
		warningsBeforeAbort: warningsBeforeAbort,
		volatileKeySet:      volatileSet,
	}
}

// Inspect checks a tool call for repetition.
func (g *ToolLoopGuard) Inspect(input ToolLoopInput) ToolLoopResult {
	if g == nil {
		return ToolLoopResult{
			Hash: computeToolLoopHash(input, nil),
		}
	}

	hash := computeToolLoopHash(input, g.volatileKeySet)

	g.mu.Lock()
	defer g.mu.Unlock()

	if hash == g.lastHash {
		g.repeatCount++
	} else {
		g.lastHash = hash
		g.repeatCount = 1
	}

	if g.breachHash != hash {
		g.breachHash = hash
		g.breachCount = 0
	}

	warn := false
	abort := false
	if g.repeatCount > g.repeatThreshold {
		if g.breachCount < g.warningsBeforeAbort {
			g.breachCount++
			warn = true
			g.lastHash = ""
			g.repeatCount = 0
		} else {
			g.breachCount++
			abort = true
		}
	}

	return ToolLoopResult{
		Hash:        hash,
		RepeatCount: g.repeatCount,
		BreachCount: g.breachCount,
		Warn:        warn,
		Abort:       abort,
	}
}

// Reset clears the guard state.
func (g *ToolLoopGuard) Reset() {
	if g == nil {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.lastHash = ""
	g.repeatCount = 0
	g.breachCount = 0
	g.breachHash = ""
}

func normalizeKeyName(key string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(strings.ToLower(key)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isVolatileKey(key string, volatileSet map[string]struct{}) bool {
	normalized := normalizeKeyName(key)
	if normalized == "" {
		return false
	}
	if _, ok := volatileSet[normalized]; ok {
		return true
	}
	for _, suffix := range volatileKeySuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func normalizeToolLoopValue(value any, volatileSet map[string]struct{}) any {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return v
	case float64:
		return v
	case json.Number:
		return v.String()
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = normalizeToolLoopValue(item, volatileSet)
		}
		return result
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(v))
		for _, k := range keys {
			if isVolatileKey(k, volatileSet) {
				continue
			}
			normalized := normalizeToolLoopValue(v[k], volatileSet)
			if normalized != nil {
				result[k] = normalized
			}
		}
		return result
	default:
		return fmt.Sprintf("%v", v)
	}
}

func computeToolLoopHash(input ToolLoopInput, volatileSet map[string]struct{}) string {
	payload := map[string]any{
		"toolName": strings.TrimSpace(input.ToolName),
		"input":    normalizeToolLoopValue(input.Input, volatileSet),
	}
	serialized, _ := json.Marshal(payload)
	h := sha256.Sum256(serialized)
	return hex.EncodeToString(h[:])
}

// --- Helper to check for repetitions in text ---

func isNonEmptyString(s string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(s)) > 0
}

// Guard wraps tools with tool loop detection. Returns a wrapper execute function.
func (g *ToolLoopGuard) Guard(toolName string, input any) (warn bool, abort bool) {
	result := g.Inspect(ToolLoopInput{ToolName: toolName, Input: input})
	return result.Warn, result.Abort
}
