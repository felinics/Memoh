package memllm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
)

const (
	defaultTimeout   = models.DefaultProviderRequestTimeout
	maxExtractFacts  = 10
	maxDecideActions = 20
)

// Config holds model resolution details for the memory LLM.
type Config struct {
	ModelID               string
	BaseURL               string
	APIKey                string `json:"-"`
	ClientType            string
	ChatCompletionsCompat string
	Timeout               time.Duration
	PromptCacheTTL        string
}

// Client implements adapters.LLM using the Twilight AI SDK.
type Client struct {
	cfg Config
}

// New creates a memory LLM client.
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Client{cfg: cfg}
}

func (c *Client) model() *sdk.Model {
	return models.NewSDKChatModel(models.SDKModelConfig{
		ModelID:               c.cfg.ModelID,
		ClientType:            c.cfg.ClientType,
		APIKey:                c.cfg.APIKey,
		BaseURL:               c.cfg.BaseURL,
		ChatCompletionsCompat: c.cfg.ChatCompletionsCompat,
	})
}

func (c *Client) Extract(ctx context.Context, req adapters.ExtractRequest) (adapters.ExtractResponse, error) {
	if len(req.Messages) == 0 {
		return adapters.ExtractResponse{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	var sb strings.Builder
	for i, m := range req.Messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		fmt.Fprintf(&sb, "[message_index=%d] ", i)
		sb.WriteString(strings.ToUpper(role[:1]) + role[1:])
		sb.WriteString(": ")
		sb.WriteString(content)
		sb.WriteString("\n")
	}
	transcript := strings.TrimSpace(sb.String())
	if transcript == "" {
		return adapters.ExtractResponse{}, nil
	}

	now := time.Now()
	if req.TimezoneLocation != nil {
		now = now.In(req.TimezoneLocation)
	}
	systemPrompt := strings.ReplaceAll(memoryExtractPrompt, "{{today}}", now.Format("2006-01-02"))

	model := c.model()
	system, messages, _ := models.ApplyPromptCache(
		model, c.cfg.PromptCacheTTL,
		systemPrompt, []sdk.Message{sdk.UserMessage(transcript)}, nil,
	)
	result, err := sdk.GenerateTextResult(ctx,
		sdk.WithModel(model),
		sdk.WithSystem(system),
		sdk.WithMessages(messages),
	)
	if err != nil {
		return adapters.ExtractResponse{}, fmt.Errorf("extract: %w", err)
	}

	extracted := parseExtractResponseWithSources(result.Text)
	if len(extracted) > maxExtractFacts {
		extracted = extracted[:maxExtractFacts]
	}
	facts := make([]string, 0, len(extracted))
	sources := make([][]string, 0, len(extracted))
	for _, fact := range extracted {
		refs := make([]string, 0, len(fact.MessageIndices))
		for _, index := range fact.MessageIndices {
			if index < 0 || index >= len(req.Messages) {
				continue
			}
			refs = append(refs, req.Messages[index].SourceMessageID)
		}
		facts = append(facts, fact.Text)
		sources = append(sources, adapters.NormalizeSourceRefs(refs))
	}
	return adapters.ExtractResponse{Facts: facts, FactSourceMessageIDs: sources}, nil
}

func (c *Client) Decide(ctx context.Context, req adapters.DecideRequest) (adapters.DecideResponse, error) {
	if len(req.Facts) == 0 {
		return adapters.DecideResponse{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	userMessage := buildUpdateUserMessage(req.Candidates, req.Facts)

	model := c.model()
	system, messages, _ := models.ApplyPromptCache(
		model, c.cfg.PromptCacheTTL,
		memoryUpdatePrompt, []sdk.Message{sdk.UserMessage(userMessage)}, nil,
	)
	result, err := sdk.GenerateTextResult(ctx,
		sdk.WithModel(model),
		sdk.WithSystem(system),
		sdk.WithMessages(messages),
	)
	if err != nil {
		return adapters.DecideResponse{}, fmt.Errorf("decide: %w", err)
	}

	actions := parseUpdateResponse(result.Text)
	if len(actions) > maxDecideActions {
		actions = actions[:maxDecideActions]
	}
	return adapters.DecideResponse{Actions: actions}, nil
}

func (c *Client) Compact(ctx context.Context, req adapters.CompactRequest) (adapters.CompactResponse, error) {
	if len(req.Memories) == 0 {
		return adapters.CompactResponse{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	payload, err := json.Marshal(map[string]any{
		"memories":     req.Memories,
		"target_count": req.TargetCount,
		"decay_days":   req.DecayDays,
	})
	if err != nil {
		return adapters.CompactResponse{}, fmt.Errorf("compact: marshal input: %w", err)
	}
	model := c.model()
	system, messages, _ := models.ApplyPromptCache(
		model, c.cfg.PromptCacheTTL,
		compactSystemPrompt, []sdk.Message{sdk.UserMessage(string(payload))}, nil,
	)
	result, err := sdk.GenerateTextResult(ctx,
		sdk.WithModel(model),
		sdk.WithSystem(system),
		sdk.WithMessages(messages),
	)
	if err != nil {
		return adapters.CompactResponse{}, fmt.Errorf("compact: %w", err)
	}
	facts := parseJSONStringArray(result.Text)
	return adapters.CompactResponse{Facts: facts}, nil
}

// buildUpdateUserMessage formats the Decide user message following Mem0's
// update prompt convention: current memory + retrieved facts in triple backticks.
func buildUpdateUserMessage(candidates []adapters.CandidateMemory, facts []string) string {
	var sb strings.Builder

	if len(candidates) > 0 {
		sb.WriteString("Below is the current content of my memory which I have collected till now. You have to update it in the following format only:\n\n```\n")
		oldMem := make([]map[string]string, 0, len(candidates))
		for _, c := range candidates {
			oldMem = append(oldMem, map[string]string{
				"id":   c.ID,
				"text": c.Memory,
			})
		}
		raw, _ := json.MarshalIndent(oldMem, "", "  ")
		sb.Write(raw)
		sb.WriteString("\n```\n\n")
	} else {
		sb.WriteString("Current memory is empty.\n\n")
	}

	sb.WriteString("The new retrieved facts are mentioned in the triple backticks. You have to analyze the new retrieved facts and determine whether these facts should be added, updated, or deleted in the memory.\n\n```\n")
	indexedFacts := make([]map[string]any, 0, len(facts))
	for i, fact := range facts {
		indexedFacts = append(indexedFacts, map[string]any{"index": i, "text": fact})
	}
	factsJSON, _ := json.Marshal(indexedFacts)
	sb.Write(factsJSON)
	sb.WriteString("\n```\n\n")

	sb.WriteString(`You must return your response in the following JSON structure only:

{
  "memory" : [
    {
      "id" : " ",
      "text" : " ",
      "event" : " ",
      "old_memory" : " ",
      "source_fact_indices" : [0]
    }
  ]
}

Follow the instruction mentioned below:
- Do not return anything from the custom few shot prompts provided above.
- If the current memory is empty, then you have to add the new retrieved facts to the memory.
- You should return the updated memory in only JSON format as shown below. The memory key should be the same if no changes are made.
- If there is an addition, generate a new key and add the new memory corresponding to it.
- If there is a deletion, the memory key-value pair should be removed from the memory.
- If there is an update, the ID key should remain the same and only the value needs to be updated.
- Every ADD or UPDATE must include source_fact_indices containing the zero-based indices of the retrieved facts that support the resulting memory. Never invent an index.

Do not return anything except the JSON format.
`)
	return sb.String()
}

// --- JSON parsing helpers ---

type extractedFactEntry struct {
	Text           string `json:"text"`
	MessageIndices []int  `json:"message_indices"`
}

// parseExtractResponseWithSources parses the provenance-aware extraction
// shape and remains compatible with legacy arrays of strings.
func parseExtractResponseWithSources(text string) []extractedFactEntry {
	text = extractJSONBlock(text)

	var objectWrapper struct {
		Facts []extractedFactEntry `json:"facts"`
	}
	if json.Unmarshal([]byte(text), &objectWrapper) == nil && len(objectWrapper.Facts) > 0 {
		return filterExtractedFactEntries(objectWrapper.Facts)
	}
	var objectArray []extractedFactEntry
	if json.Unmarshal([]byte(text), &objectArray) == nil && len(objectArray) > 0 {
		return filterExtractedFactEntries(objectArray)
	}

	var legacyWrapper struct {
		Facts []string `json:"facts"`
	}
	legacy := []string(nil)
	if json.Unmarshal([]byte(text), &legacyWrapper) == nil {
		legacy = filterNonEmpty(legacyWrapper.Facts)
	}
	if len(legacy) == 0 {
		legacy = parseJSONStringArray(text)
	}
	entries := make([]extractedFactEntry, 0, len(legacy))
	for _, fact := range legacy {
		entries = append(entries, extractedFactEntry{Text: fact})
	}
	return entries
}

// parseExtractResponse is retained for parser compatibility tests and callers
// that only need fact text.
func parseExtractResponse(text string) []string {
	entries := parseExtractResponseWithSources(text)
	facts := make([]string, 0, len(entries))
	for _, entry := range entries {
		facts = append(facts, entry.Text)
	}
	return facts
}

func filterExtractedFactEntries(entries []extractedFactEntry) []extractedFactEntry {
	out := make([]extractedFactEntry, 0, len(entries))
	for _, entry := range entries {
		entry.Text = strings.TrimSpace(entry.Text)
		if entry.Text == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func parseJSONStringArray(text string) []string {
	text = extractJSONBlock(text)
	var facts []string
	if json.Unmarshal([]byte(text), &facts) == nil {
		return filterNonEmpty(facts)
	}
	return nil
}

// updateResponseEntry mirrors a single item in Mem0's {"memory": [...]} response.
type updateResponseEntry struct {
	ID                string `json:"id"`
	Text              string `json:"text"`
	Event             string `json:"event"`
	OldMemory         string `json:"old_memory"`
	SourceFactIndices []int  `json:"source_fact_indices"`
}

// parseUpdateResponse parses the {"memory": [...]} response from Decide.
func parseUpdateResponse(text string) []adapters.DecisionAction {
	text = extractJSONBlock(text)

	var wrapper struct {
		Memory []updateResponseEntry `json:"memory"`
	}
	if json.Unmarshal([]byte(text), &wrapper) == nil && len(wrapper.Memory) > 0 {
		actions := make([]adapters.DecisionAction, 0, len(wrapper.Memory))
		for _, entry := range wrapper.Memory {
			event := strings.ToUpper(strings.TrimSpace(entry.Event))
			if event == "NONE" {
				event = "NOOP"
			}
			actions = append(actions, adapters.DecisionAction{
				Event:             event,
				ID:                strings.TrimSpace(entry.ID),
				Text:              strings.TrimSpace(entry.Text),
				OldMemory:         strings.TrimSpace(entry.OldMemory),
				SourceFactIndices: entry.SourceFactIndices,
			})
		}
		return actions
	}

	var flat []adapters.DecisionAction
	if json.Unmarshal([]byte(text), &flat) == nil {
		return flat
	}
	return nil
}

func extractJSONBlock(text string) string {
	text = strings.TrimSpace(text)
	if start := strings.Index(text, "```json"); start >= 0 {
		text = text[start+7:]
		if end := strings.Index(text, "```"); end >= 0 {
			text = text[:end]
		}
	} else if start := strings.Index(text, "```"); start >= 0 {
		text = text[start+3:]
		if end := strings.Index(text, "```"); end >= 0 {
			text = text[:end]
		}
	}
	text = strings.TrimSpace(text)
	if len(text) > 0 && text[0] != '{' && text[0] != '[' {
		braceIdx := strings.IndexByte(text, '{')
		bracketIdx := strings.IndexByte(text, '[')
		cutIdx := -1
		switch {
		case braceIdx >= 0 && bracketIdx >= 0:
			cutIdx = min(braceIdx, bracketIdx)
		case braceIdx >= 0:
			cutIdx = braceIdx
		case bracketIdx >= 0:
			cutIdx = bracketIdx
		}
		if cutIdx >= 0 {
			text = text[cutIdx:]
		}
	}
	return text
}

func filterNonEmpty(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

const compactSystemPrompt = `You are a long-term memory compaction assistant. The user message is JSON with "memories", "target_count", and optional "decay_days".

Goal:
- Return roughly target_count durable memory entries; prefer being slightly under target_count over keeping weak entries.
- Cluster related memories by topic and merge each cluster into one standalone, information-dense sentence when natural.
- Preserve safety-critical facts, stable preferences, identity/profile facts, active plans with dates, and explicit user instructions.
- Resolve conflicts using recency, explicit "current/changed" wording, and stronger evidence; keep previous values only when the change history is itself useful.
- Drop duplicates, stale low-value details, transient test/debug/acceptance artifacts, and facts that are not useful as long-term memory.
- Apply decay_days as a priority signal: old memories are easier to merge or drop unless they are safety-critical or clearly durable.
- Do not invent facts, IDs, dates, causes, or preferences not present in the input.

Output rules:
- Return a JSON array only.
- Each array item must be a concise fact string.
- Do not wrap the JSON in Markdown or add explanatory text.`
