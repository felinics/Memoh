package contextfrag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

// KindToolDefinition labels the serialized definition of one tool the run
// registered with the provider. It is never a fragment; it exists so tool
// schemas can share the content-addressed text store with fragments.
const KindToolDefinition Kind = "tool_definition"

// FragmentText is the rendered text of one context fragment. ContentHash is
// the fragment's own (scope-bound) hash the manifest and selection audit
// carry; TextHash is the store key, derived from the kind and the text alone
// so one text is stored once however many sessions or messages render it.
type FragmentText struct {
	ContentHash string
	TextHash    string
	Kind        Kind
	// Label names the fragment as the assembler did (system.prompt.body,
	// workspace/exec); it travels with the text, never with the snapshot.
	Label string
	Text  string
}

// FragmentTextSink receives rendered fragment texts to persist. Implementations
// must not block the run: the holder calls them inline on the turn path. A
// sink is bound to its run when it is attached, so no context travels with
// the texts.
type FragmentTextSink interface {
	PersistFragmentTexts(texts []FragmentText)
}

// FragmentRef is the bounded per-run record of one injected fragment: enough to
// find its text in the content-addressed store and account for it. Neither the
// text nor the fragment's own name is part of it; the snapshot stays
// content-light. TextHash is the store key of the text this run recorded;
// it is empty when the run stored no text for the fragment.
type FragmentRef struct {
	Kind          Kind   `json:"kind"`
	Slot          Slot   `json:"slot"`
	ContentHash   string `json:"content_hash,omitempty"`
	TextHash      string `json:"text_hash,omitempty"`
	TokenEstimate int    `json:"token_estimate,omitempty"`
	TextBytes     int    `json:"text_bytes,omitempty"`
}

// injectedFragment reports whether a fragment is context the runtime put in
// front of the model rather than a conversation message the history already
// keeps. Recalled memory is injected wherever it lands: the history slot
// only positions it, and no persisted message holds its text.
func injectedFragment(kind Kind, slot Slot) bool {
	if kind == KindMemoryRecall {
		return true
	}
	if slot == SlotHistory || slot == SlotCurrentUser {
		return false
	}
	return kind != KindConversationEvent && kind != KindCurrentUserMessage
}

func fragmentText(frag ContextFrag) string {
	var chunks []string
	for _, part := range frag.Parts {
		switch part.Type {
		case PartText:
			if part.Text != "" {
				chunks = append(chunks, part.Text)
			}
		case PartSDKMessage:
			if message := partMessage(part); message != nil {
				for _, content := range message.Content {
					if text, ok := content.(sdk.TextPart); ok && text.Text != "" {
						chunks = append(chunks, text.Text)
					}
				}
			}
		}
	}
	return strings.Join(chunks, "\n")
}

// TextHash is the content-addressed key of a stored fragment text: the kind
// and the rendered text, nothing scope-bound.
func TextHash(kind Kind, text string) string {
	digest := sha256.New()
	_, _ = io.WriteString(digest, string(kind))
	digest.Write([]byte{0})
	_, _ = io.WriteString(digest, text)
	return hex.EncodeToString(digest.Sum(nil))
}

func fragmentContentHash(frag ContextFrag) string {
	if hash := strings.TrimSpace(frag.Ref.ContentHash); hash != "" {
		return hash
	}
	hash, err := CanonicalFragmentHash(frag)
	if err != nil {
		return ""
	}
	return hash.Value
}

// FragmentTexts extracts the texts of the injected fragments, keyed by their
// content hash. Conversation messages are skipped: the history already stores
// them.
func FragmentTexts(frags []ContextFrag) []FragmentText {
	var texts []FragmentText
	for _, frag := range frags {
		if !injectedFragment(frag.Kind, frag.Slot) {
			continue
		}
		text := fragmentText(frag)
		hash := fragmentContentHash(frag)
		if text == "" || hash == "" {
			continue
		}
		texts = append(texts, FragmentText{ContentHash: hash, TextHash: TextHash(frag.Kind, text), Kind: frag.Kind, Label: frag.ID, Text: text})
	}
	return texts
}

// ToolDefinitionText measures one tool definition as the provider receives it
// and returns the serialized definition addressed by its hash.
func ToolDefinitionText(provider string, tool sdk.Tool) (ToolDefAccounting, FragmentText) {
	data, err := json.Marshal(tool)
	if err != nil {
		size := len(tool.Name) + len(tool.Description)
		return ToolDefAccounting{Provider: provider, Name: tool.Name, Bytes: size, TokenEstimate: TokensFromBytes(size)}, FragmentText{}
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	return ToolDefAccounting{
			Provider:      provider,
			Name:          tool.Name,
			Bytes:         len(data),
			TokenEstimate: TokensFromBytes(len(data)),
			ContentHash:   hash,
		}, FragmentText{
			ContentHash: hash,
			TextHash:    hash,
			Kind:        KindToolDefinition,
			Label:       provider + "/" + tool.Name,
			Text:        string(data),
		}
}

// fragmentRefs lists the injected fragments of a manifest in manifest order.
func fragmentRefs(items []ManifestItem) []FragmentRef {
	var refs []FragmentRef
	for _, item := range items {
		if !injectedFragment(item.Kind, item.Slot) {
			continue
		}
		refs = append(refs, FragmentRef{
			Kind:          item.Kind,
			Slot:          item.Slot,
			ContentHash:   item.Ref.ContentHash,
			TokenEstimate: item.TokenEstimate,
			TextBytes:     item.TextBytes,
		})
	}
	return refs
}

// SetTextSink attaches the run-bound store that receives rendered fragment
// texts.
func (h *LifecycleHolder) SetTextSink(sink FragmentTextSink) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.textSink = sink
	h.mu.Unlock()
}

// RecordFragmentTexts hands the injected fragments' texts to the sink once per
// text hash for this run, so per-step refreshes cost a map lookup each, and
// remembers which store key each fragment resolved to for the snapshot.
func (h *LifecycleHolder) RecordFragmentTexts(frags []ContextFrag) {
	if h == nil || len(frags) == 0 {
		return
	}
	h.recordTexts(FragmentTexts(frags))
}

// RecordToolDefinitions hands serialized tool definitions to the sink.
func (h *LifecycleHolder) RecordToolDefinitions(texts []FragmentText) {
	if h == nil || len(texts) == 0 {
		return
	}
	h.recordTexts(texts)
}

func (h *LifecycleHolder) recordTexts(texts []FragmentText) {
	h.mu.Lock()
	sink := h.textSink
	if sink == nil {
		h.mu.Unlock()
		return
	}
	if h.recordedTexts == nil {
		h.recordedTexts = make(map[string]struct{}, len(texts))
		h.textHashes = make(map[string]string, len(texts))
	}
	fresh := make([]FragmentText, 0, len(texts))
	for _, text := range texts {
		if text.TextHash == "" {
			continue
		}
		if text.ContentHash != "" {
			h.textHashes[text.ContentHash] = text.TextHash
		}
		if _, seen := h.recordedTexts[text.TextHash]; seen {
			continue
		}
		h.recordedTexts[text.TextHash] = struct{}{}
		fresh = append(fresh, text)
	}
	h.mu.Unlock()
	if len(fresh) == 0 {
		return
	}
	sink.PersistFragmentTexts(fresh)
}
