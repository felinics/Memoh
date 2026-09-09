package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/migrate"
	"github.com/felinics/memoh/internal/settings"
)

// MemoryHandler handles memory CRUD operations scoped by bot.
type MemoryHandler struct {
	botService      *bots.Service
	accountService  *accounts.Service
	settingsService *settings.Service
	memoryRegistry  *memprovider.Registry
	logger          *slog.Logger
}

type memoryAddPayload struct {
	Message          string                `json:"message,omitempty"`
	Messages         []memprovider.Message `json:"messages,omitempty"`
	Namespace        string                `json:"namespace,omitempty"`
	RunID            string                `json:"run_id,omitempty"`
	Metadata         map[string]any        `json:"metadata,omitempty"`
	Filters          map[string]any        `json:"filters,omitempty"`
	Infer            *bool                 `json:"infer,omitempty"`
	EmbeddingEnabled *bool                 `json:"embedding_enabled,omitempty"`
}

type memorySearchPayload struct {
	Query            string         `json:"query"`
	RunID            string         `json:"run_id,omitempty"`
	Limit            int            `json:"limit,omitempty"`
	Filters          map[string]any `json:"filters,omitempty"`
	Sources          []string       `json:"sources,omitempty"`
	EmbeddingEnabled *bool          `json:"embedding_enabled,omitempty"`
	NoStats          bool           `json:"no_stats,omitempty"`
}

type memoryDeletePayload struct {
	MemoryIDs []string `json:"memory_ids,omitempty"`
}

type memoryUpdatePayload struct {
	Memory string `json:"memory"`
}

type memoryCompactPayload struct {
	Ratio     float64 `json:"ratio"`
	DecayDays *int    `json:"decay_days,omitempty"`
}

// namespaceScope holds namespace + scopeId for a single memory scope.
type namespaceScope struct {
	Namespace string
	ScopeID   string
}

const (
	sharedMemoryNamespace    = "bot"
	defaultBuiltinProviderID = memprovider.DefaultBuiltinProviderID
)

// NewMemoryHandler creates a MemoryHandler.
func NewMemoryHandler(log *slog.Logger, botService *bots.Service, accountService *accounts.Service) *MemoryHandler {
	return &MemoryHandler{
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "memory")),
	}
}

// SetMemoryRegistry sets the provider registry for provider-based memory operations.
func (h *MemoryHandler) SetMemoryRegistry(registry *memprovider.Registry) {
	h.memoryRegistry = registry
}

// SetSettingsService sets the settings service for provider resolution.
func (h *MemoryHandler) SetSettingsService(svc *settings.Service) {
	h.settingsService = svc
}

// resolveProvider returns the memory provider for a bot. An explicitly selected
// provider must be available; only bots without a selected provider may fall
// back to the builtin default.
func (h *MemoryHandler) resolveProvider(ctx context.Context, botID string) (memprovider.Provider, error) {
	if h.memoryRegistry == nil {
		return nil, nil
	}
	if h.settingsService != nil {
		botSettings, err := h.settingsService.GetBot(ctx, botID)
		if err == nil {
			providerID := strings.TrimSpace(botSettings.MemoryProviderID)
			if providerID != "" {
				p, getErr := h.memoryRegistry.Get(ctx, providerID)
				if getErr == nil {
					return p, nil
				}
				h.logger.Warn("memory provider lookup failed", slog.String("provider_id", providerID), slog.Any("error", getErr))
				return nil, fmt.Errorf("configured memory provider is unavailable: %w", getErr)
			}
		}
	}
	p, err := h.memoryRegistry.Get(ctx, defaultBuiltinProviderID)
	if err != nil {
		return nil, nil
	}
	return p, nil
}

// Register registers chat-level memory routes.
func (h *MemoryHandler) Register(e *echo.Echo) {
	chatGroup := e.Group("/bots/:bot_id/memory")
	chatGroup.POST("", h.ChatAdd)
	chatGroup.POST("/search", h.ChatSearch)
	chatGroup.POST("/compact", h.ChatCompact)
	chatGroup.POST("/rebuild", h.ChatRebuild)
	chatGroup.POST("/ingest", h.ChatIngest)
	chatGroup.GET("/status", h.ChatStatus)
	chatGroup.GET("", h.ChatGetAll)
	chatGroup.GET("/usage", h.ChatUsage)
	chatGroup.GET("/graph", h.ChatGraph)
	chatGroup.DELETE("", h.ChatDelete)
	chatGroup.PUT("/:memory_id", h.ChatUpdate)
	chatGroup.DELETE("/:memory_id", h.ChatDeleteOne)
}

func (h *MemoryHandler) checkService(ctx context.Context, botID string) (memprovider.Provider, error) {
	p, err := h.resolveProvider(ctx, botID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	if p != nil {
		return p, nil
	}
	return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "memory service not available")
}

// --- Bot-level memory endpoints ---

// ChatAdd godoc
// @Summary Add memory
// @Description Add memory into the bot-shared namespace
// @Tags memory
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body memoryAddPayload true "Memory add payload"
// @Success 200 {object} adapters.SearchResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory [post].
func (h *MemoryHandler) ChatAdd(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}

	var payload memoryAddPayload
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	namespace, err := normalizeSharedMemoryNamespace(payload.Namespace)
	if err != nil {
		return err
	}

	scopeID, resolvedBotID, err := h.resolveWriteScope(botID)
	if err != nil {
		return err
	}

	filters := buildNamespaceFilters(namespace, scopeID, payload.Filters)
	channelIdentityID, identityErr := h.requireChannelIdentityID(c)
	if identityErr != nil {
		return identityErr
	}
	req := memprovider.AddRequest{
		Message:          payload.Message,
		Messages:         payload.Messages,
		BotID:            resolvedBotID,
		RunID:            payload.RunID,
		Metadata:         memprovider.MergeMetadata(payload.Metadata, memprovider.BuildProfileMetadata("", channelIdentityID, "")),
		Filters:          filters,
		Infer:            payload.Infer,
		EmbeddingEnabled: payload.EmbeddingEnabled,
	}

	provider, checkErr := h.checkService(c.Request().Context(), resolvedBotID)
	if checkErr != nil {
		return checkErr
	}
	resp, err := provider.Add(c.Request().Context(), req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// ChatSearch godoc
// @Summary Search memory
// @Description Search memory in the bot-shared namespace
// @Tags memory
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body memorySearchPayload true "Memory search payload"
// @Success 200 {object} adapters.SearchResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory/search [post].
func (h *MemoryHandler) ChatSearch(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}

	var payload memorySearchPayload
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	scopes, err := h.resolveEnabledScopes(botID)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}

	results := make([]memprovider.MemoryItem, 0)
	for _, scope := range scopes {
		filters := buildNamespaceFilters(scope.Namespace, scope.ScopeID, payload.Filters)
		req := memprovider.SearchRequest{
			Query:            payload.Query,
			BotID:            botID,
			RunID:            payload.RunID,
			Limit:            payload.Limit,
			Filters:          filters,
			Sources:          payload.Sources,
			EmbeddingEnabled: payload.EmbeddingEnabled,
			NoStats:          payload.NoStats,
		}
		resp, searchErr := provider.Search(c.Request().Context(), req)
		if searchErr != nil {
			h.logger.Warn("search namespace failed", slog.String("namespace", scope.Namespace), slog.Any("error", searchErr))
			continue
		}
		results = append(results, resp.Results...)
	}
	results = deduplicateMemoryItems(botID, results)
	return c.JSON(http.StatusOK, memprovider.SearchResponse{Results: results})
}

// ChatGetAll godoc
// @Summary Get all memories
// @Description List all memories in the bot-shared namespace
// @Tags memory
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param no_stats query bool false "Skip optional stats in memory search response"
// @Success 200 {object} adapters.SearchResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory [get].
func (h *MemoryHandler) ChatGetAll(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}

	noStats := strings.EqualFold(c.QueryParam("no_stats"), "true")
	scopes, err := h.resolveEnabledScopes(botID)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}

	var allResults []memprovider.MemoryItem
	for _, scope := range scopes {
		req := memprovider.GetAllRequest{
			Filters: buildNamespaceFilters(scope.Namespace, scope.ScopeID, nil),
			NoStats: noStats,
		}
		resp, getAllErr := provider.GetAll(c.Request().Context(), req)
		if getAllErr != nil {
			h.logger.Warn("getall namespace failed", slog.String("namespace", scope.Namespace), slog.Any("error", getAllErr))
			continue
		}
		allResults = append(allResults, resp.Results...)
	}
	allResults = deduplicateMemoryItems(botID, allResults)

	return c.JSON(http.StatusOK, memprovider.SearchResponse{Results: allResults})
}

// graphNode is one node in the memory graph view.
type graphNode struct {
	ID        string         `json:"id"`
	Label     string         `json:"label"`
	Slug      string         `json:"slug"`
	Memory    string         `json:"memory"`
	Subject   string         `json:"subject,omitempty"`
	Topic     string         `json:"topic,omitempty"`
	Count     int            `json:"count,omitempty"`
	MemoryIDs []string       `json:"memory_ids,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// graphEdge is one edge in the memory graph view.
type graphEdge struct {
	Source string   `json:"source"`
	Target string   `json:"target"`
	Rel    string   `json:"rel"`
	Rels   []string `json:"rels,omitempty"`
	Count  int      `json:"count"`
	Weight float64  `json:"weight"`
}

// graphResponse is the payload for the memory graph view.
type graphResponse struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

// ChatGraph returns the memory graph (nodes + derived edges) for the wiki
// visualization. The edge derivation uses the same migrate.PlanFromNodes path as
// the graph runtime/store, so the API view and recall graph do not drift.
//
// @Summary Get memory graph
// @Description Get derived memory graph nodes and edges for a bot.
// @Tags memory
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} graphResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory/graph [get].
func (h *MemoryHandler) ChatGraph(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	scopes, err := h.resolveEnabledScopes(botID)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}

	var allResults []memprovider.MemoryItem
	for _, scope := range scopes {
		resp, getAllErr := provider.GetAll(c.Request().Context(), memprovider.GetAllRequest{
			Filters: buildNamespaceFilters(scope.Namespace, scope.ScopeID, nil),
			NoStats: true,
		})
		if getAllErr != nil {
			continue
		}
		allResults = append(allResults, resp.Results...)
	}
	allResults = deduplicateMemoryItems(botID, allResults)

	nodes, nodeSpecs, sourceToConcept := buildGraphProjection(botID, allResults)
	edges := aggregateGraphEdges(projectGraphEdges(migrate.PlanFromNodes(nodeSpecs), sourceToConcept))

	return c.JSON(http.StatusOK, graphResponse{Nodes: nodes, Edges: edges})
}

func buildGraphProjection(botID string, items []memprovider.MemoryItem) ([]graphNode, []migrate.NodeSpec, map[string]string) {
	type conceptBucket struct {
		id        string
		slug      string
		count     int
		memoryIDs []string
		item      memprovider.MemoryItem
		spec      migrate.NodeSpec
	}

	buckets := map[string]*conceptBucket{}
	order := []string{}
	nodeSpecs := make([]migrate.NodeSpec, 0, len(items))
	sourceToConcept := make(map[string]string, len(items))

	for _, item := range items {
		item = canonicalizeMemoryItem(botID, item)
		spec := memoryItemToGraphNodeSpec(botID, item)
		if spec.ID == "" {
			continue
		}
		nodeSpecs = append(nodeSpecs, spec)

		slug := migrate.NodeSlug(spec.ID, spec.Subject, spec.Topic)
		conceptID := slug
		if conceptID == "" {
			conceptID = spec.ID
		}
		sourceToConcept[spec.ID] = conceptID

		bucket := buckets[conceptID]
		if bucket == nil {
			bucket = &conceptBucket{id: conceptID, slug: slug, item: item, spec: spec}
			buckets[conceptID] = bucket
			order = append(order, conceptID)
		}
		bucket.count++
		bucket.memoryIDs = append(bucket.memoryIDs, item.ID)
		if graphMemoryItemRank(item, spec) > graphMemoryItemRank(bucket.item, bucket.spec) {
			bucket.item = item
			bucket.spec = spec
		}
	}

	nodes := make([]graphNode, 0, len(order))
	for _, conceptID := range order {
		bucket := buckets[conceptID]
		sort.Strings(bucket.memoryIDs)
		nodes = append(nodes, graphNode{
			ID:        bucket.id,
			Label:     graphNodeLabel(bucket.item, bucket.spec),
			Slug:      bucket.slug,
			Memory:    strings.TrimSpace(bucket.item.Memory),
			Subject:   bucket.spec.Subject,
			Topic:     bucket.spec.Topic,
			Count:     bucket.count,
			MemoryIDs: bucket.memoryIDs,
			Metadata:  bucket.item.Metadata,
		})
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Slug != nodes[j].Slug {
			return nodes[i].Slug < nodes[j].Slug
		}
		return nodes[i].ID < nodes[j].ID
	})
	return nodes, nodeSpecs, sourceToConcept
}

func projectGraphEdges(edges []migrate.EdgeSpec, sourceToConcept map[string]string) []migrate.EdgeSpec {
	if len(edges) == 0 {
		return nil
	}
	projected := make([]migrate.EdgeSpec, 0, len(edges))
	for _, edge := range edges {
		source := sourceToConcept[strings.TrimSpace(edge.SrcNode)]
		target := sourceToConcept[strings.TrimSpace(edge.DstNode)]
		if source == "" || target == "" || source == target {
			continue
		}
		edge.SrcNode = source
		edge.DstNode = target
		projected = append(projected, edge)
	}
	return projected
}

func graphNodeLabel(item memprovider.MemoryItem, spec migrate.NodeSpec) string {
	label := strings.TrimSpace(spec.Subject)
	if label == "" {
		label = strings.TrimSpace(spec.Topic)
	}
	if label == "" {
		label = strings.TrimSpace(item.Memory)
	}
	if len(label) > 40 {
		label = label[:37] + "..."
	}
	return label
}

func aggregateGraphEdges(edges []migrate.EdgeSpec) []graphEdge {
	type edgeBucket struct {
		source string
		target string
		count  int
		weight float64
		rels   map[string]float64
	}

	buckets := make(map[string]*edgeBucket)
	for _, edge := range edges {
		source := strings.TrimSpace(edge.SrcNode)
		target := strings.TrimSpace(edge.DstNode)
		if source == "" || target == "" || source == target {
			continue
		}
		if target < source {
			source, target = target, source
		}

		key := source + "\x00" + target
		bucket := buckets[key]
		if bucket == nil {
			bucket = &edgeBucket{
				source: source,
				target: target,
				rels:   map[string]float64{},
			}
			buckets[key] = bucket
		}

		rel := strings.TrimSpace(string(edge.Rel))
		if rel == "" {
			rel = "related"
		}
		weight := float64(edge.Weight)
		if weight <= 0 {
			weight = 1
		}
		bucket.count++
		bucket.weight += weight
		bucket.rels[rel] += weight
	}

	out := make([]graphEdge, 0, len(buckets))
	for _, bucket := range buckets {
		rels := make([]string, 0, len(bucket.rels))
		for rel := range bucket.rels {
			rels = append(rels, rel)
		}
		sort.Slice(rels, func(i, j int) bool {
			left := bucket.rels[rels[i]]
			right := bucket.rels[rels[j]]
			if left != right {
				return left > right
			}
			if graphRelRank(rels[i]) != graphRelRank(rels[j]) {
				return graphRelRank(rels[i]) < graphRelRank(rels[j])
			}
			return rels[i] < rels[j]
		})
		primaryRel := ""
		if len(rels) > 0 {
			primaryRel = rels[0]
		}
		out = append(out, graphEdge{
			Source: bucket.source,
			Target: bucket.target,
			Rel:    primaryRel,
			Rels:   rels,
			Count:  bucket.count,
			Weight: bucket.weight,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Target < out[j].Target
	})
	return out
}

func graphRelRank(rel string) int {
	switch migrate.EdgeRel(rel) {
	case migrate.EdgeRefs:
		return 0
	case migrate.EdgeSameProfile:
		return 1
	case migrate.EdgeSameTopic:
		return 2
	case migrate.EdgeSameDay:
		return 3
	default:
		return 100
	}
}

func memoryItemToGraphNodeSpec(botID string, item memprovider.MemoryItem) migrate.NodeSpec {
	metadata := item.Metadata
	layer := migrate.LayerNote
	if raw, ok := metadata["layer"].(string); ok && strings.TrimSpace(raw) != "" {
		switch migrate.MemoryLayer(strings.ToLower(strings.TrimSpace(raw))) {
		case migrate.LayerPreference, migrate.LayerIdentity, migrate.LayerContext,
			migrate.LayerExperience, migrate.LayerActivity, migrate.LayerPersona, migrate.LayerNote:
			layer = migrate.MemoryLayer(strings.TrimSpace(raw))
		}
	}
	profileRef := graphMetadataString(metadata, "profile_ref")
	if profileRef == "" {
		profileRef = graphMetadataString(metadata, "profile_user_id")
	}
	return migrate.NodeSpec{
		ID:         strings.TrimSpace(item.ID),
		BotID:      botID,
		Body:       strings.TrimSpace(item.Memory),
		Hash:       strings.TrimSpace(item.Hash),
		Layer:      layer,
		Subject:    graphMetadataString(metadata, "subject"),
		Confidence: graphMetadataFloat(metadata, "confidence", 0.5),
		Metadata:   metadata,
		ProfileRef: profileRef,
		Topic:      graphMetadataString(metadata, "topic"),
		CapturedAt: graphParseTime(item.CreatedAt),
	}
}

func graphMetadataString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func graphMetadataFloat(m map[string]any, key string, def float32) float32 {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		f := float32(n)
		if f >= 0 && f <= 1 {
			return f
		}
	case float32:
		if n >= 0 && n <= 1 {
			return n
		}
	}
	return def
}

func graphParseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// requireMemoryOwnedByBot rejects memory IDs whose bot prefix does not match
// the authorized bot. Builtin providers derive the target bot from the memory
// ID prefix ("<botID>:mem_..."), not from the authorized bot, so without this
// check /bots/A/memory/B:mem_x would operate on bot B's memory. The prefix is
// parsed exactly like the provider does (everything before the first ":").
func requireMemoryOwnedByBot(botID, memoryID string) error {
	parts := strings.SplitN(strings.TrimSpace(memoryID), ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) != botID {
		return echo.NewHTTPError(http.StatusForbidden, "memory does not belong to this bot")
	}
	return nil
}

func memoryIDFromPath(c echo.Context) string {
	memoryID := strings.TrimSpace(c.Param("memory_id"))
	if c.Request().URL.RawPath == "" {
		return memoryID
	}
	if decoded, err := url.PathUnescape(memoryID); err == nil {
		return strings.TrimSpace(decoded)
	}
	return memoryID
}

// @Summary Delete memories
// @Description Delete specific memories by IDs, or delete all memories if no IDs are provided
// @Tags memory
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body memoryDeletePayload false "Optional: specify memory_ids to delete; if omitted, deletes all"
// @Success 200 {object} adapters.DeleteResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory [delete].
func (h *MemoryHandler) ChatDelete(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}

	var payload memoryDeletePayload
	_ = c.Bind(&payload)

	if len(payload.MemoryIDs) > 0 {
		// Reject the whole batch if any ID targets another bot's memory.
		for _, memoryID := range payload.MemoryIDs {
			if err := requireMemoryOwnedByBot(botID, memoryID); err != nil {
				return err
			}
		}
		resp, delErr := provider.DeleteBatch(c.Request().Context(), payload.MemoryIDs)
		if delErr != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, delErr.Error())
		}
		return c.JSON(http.StatusOK, resp)
	}

	scopes, err := h.resolveEnabledScopes(botID)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		req := memprovider.DeleteAllRequest{
			Filters: buildNamespaceFilters(scope.Namespace, scope.ScopeID, nil),
		}
		if _, delErr := provider.DeleteAll(c.Request().Context(), req); delErr != nil {
			h.logger.Warn("deleteall namespace failed", slog.String("namespace", scope.Namespace), slog.Any("error", delErr))
		}
	}
	return c.JSON(http.StatusOK, memprovider.DeleteResponse{Message: "All memories deleted successfully!"})
}

// ChatDeleteOne godoc
// @Summary Delete a single memory
// @Description Delete a single memory by its ID
// @Tags memory
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Memory ID"
// @Success 200 {object} adapters.DeleteResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory/{id} [delete].
func (h *MemoryHandler) ChatDeleteOne(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}

	memoryID := memoryIDFromPath(c)
	if memoryID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "memory_id is required")
	}
	if err := requireMemoryOwnedByBot(botID, memoryID); err != nil {
		return err
	}
	resp, err := provider.Delete(c.Request().Context(), memoryID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// ChatUpdate godoc
// @Summary Update a single memory by id
// @Description Update the body of an existing memory entry in place (preserves id, layer, metadata). Replaces the legacy client-side delete-then-add edit emulation.
// @Tags memory
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param memory_id path string true "Memory ID"
// @Param payload body memoryUpdatePayload true "Update request"
// @Success 200 {object} adapters.MemoryItem
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory/{memory_id} [put].
func (h *MemoryHandler) ChatUpdate(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}
	memoryID := memoryIDFromPath(c)
	if memoryID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "memory_id is required")
	}
	if err := requireMemoryOwnedByBot(botID, memoryID); err != nil {
		return err
	}
	var payload memoryUpdatePayload
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(payload.Memory) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "memory is required")
	}
	item, err := provider.Update(c.Request().Context(), memprovider.UpdateRequest{
		MemoryID: memoryID,
		Memory:   payload.Memory,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, item)
}

// ChatCompact godoc
// @Summary Compact memories
// @Description Consolidate memories by merging similar/redundant entries using LLM.
// @Description
// @Description **ratio** (required, range (0,1]):
// @Description - 0.8 = light compression, mostly dedup, keep ~80% of entries
// @Description - 0.5 = moderate compression, merge similar facts, keep ~50%
// @Description - 0.3 = aggressive compression, heavily consolidate, keep ~30%
// @Description
// @Description **decay_days** (optional): enable time decay — memories older than N days are treated as low priority and more likely to be merged/dropped.
// @Tags memory
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body memoryCompactPayload true "ratio (0,1] required; decay_days optional"
// @Success 200 {object} adapters.CompactResult
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 501 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory/compact [post].
func (h *MemoryHandler) ChatCompact(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	var payload memoryCompactPayload
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if payload.Ratio <= 0 || payload.Ratio > 1 {
		return echo.NewHTTPError(http.StatusBadRequest, "ratio is required and must be in range (0, 1]")
	}
	ratio := payload.Ratio
	var decayDays int
	if payload.DecayDays != nil && *payload.DecayDays > 0 {
		decayDays = *payload.DecayDays
	}

	scopes, err := h.resolveEnabledScopes(botID)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no memory scopes found")
	}

	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}
	capability := semanticCompactCapability(provider)
	if !capability.Semantic {
		reason := strings.TrimSpace(capability.Reason)
		if reason == "" {
			reason = "selected memory provider does not support semantic compact"
		}
		return echo.NewHTTPError(http.StatusNotImplemented, reason)
	}

	scope := scopes[0]
	filters := buildNamespaceFilters(scope.Namespace, scope.ScopeID, nil)
	result, err := provider.Compact(c.Request().Context(), filters, ratio, decayDays)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, result)
}

// ChatUsage godoc
// @Summary Get memory usage
// @Description Query the estimated storage usage of current memories
// @Tags memory
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} adapters.UsageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory/usage [get].
func (h *MemoryHandler) ChatUsage(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}

	scopes, err := h.resolveEnabledScopes(botID)
	if err != nil {
		return err
	}

	var totalUsage memprovider.UsageResponse
	for _, scope := range scopes {
		filters := buildNamespaceFilters(scope.Namespace, scope.ScopeID, nil)
		usage, usageErr := provider.Usage(c.Request().Context(), filters)
		if usageErr != nil {
			h.logger.Warn("usage namespace failed", slog.String("namespace", scope.Namespace), slog.Any("error", usageErr))
			continue
		}
		totalUsage.Count += usage.Count
		totalUsage.TotalTextBytes += usage.TotalTextBytes
		totalUsage.EstimatedStorageBytes += usage.EstimatedStorageBytes
	}
	if totalUsage.Count > 0 {
		totalUsage.AvgTextBytes = totalUsage.TotalTextBytes / int64(totalUsage.Count)
	}
	return c.JSON(http.StatusOK, totalUsage)
}

// ChatRebuild godoc
// @Summary Rebuild memories from filesystem
// @Description Read memory files from the workspace filesystem (source of truth) and restore missing entries to memory storage
// @Tags memory
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} adapters.RebuildResult
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory/rebuild [post].
func (h *MemoryHandler) ChatRebuild(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}
	syncProvider, ok := provider.(memprovider.SourceSyncProvider)
	if !ok {
		return echo.NewHTTPError(http.StatusConflict, "selected memory provider does not support rebuild from markdown source")
	}
	status, err := syncProvider.Status(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !status.CanManualSync {
		return echo.NewHTTPError(http.StatusConflict, "manual sync is not available for the selected memory provider")
	}
	result, err := syncProvider.Rebuild(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, result)
}

// ChatIngest godoc
// @Summary Ingest agent-authored memory markdown into the wiki store
// @Description Read /data/memory/*.md the bot (or its agent) wrote directly and upsert them as DB memory nodes, so they become searchable and survive the next derived-view rebuild. Idempotent (ON CONFLICT id DO UPDATE).
// @Tags memory
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} adapters.IngestResult
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory/ingest [post].
func (h *MemoryHandler) ChatIngest(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}
	ingestProvider, ok := provider.(memprovider.MarkdownIngestProvider)
	if !ok {
		return echo.NewHTTPError(http.StatusConflict, "selected memory provider does not support markdown ingest")
	}
	result, err := ingestProvider.IngestFromMarkdown(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, result)
}

// ChatStatus godoc
// @Summary Get memory runtime status
// @Description Get the resolved memory runtime status for a bot, including index health and source counts
// @Tags memory
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} adapters.MemoryStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/memory/status [get].
func (h *MemoryHandler) ChatStatus(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	provider, checkErr := h.checkService(c.Request().Context(), botID)
	if checkErr != nil {
		return checkErr
	}
	syncProvider, ok := provider.(memprovider.SourceSyncProvider)
	if !ok {
		return echo.NewHTTPError(http.StatusConflict, "selected memory provider does not expose runtime status")
	}
	status, err := syncProvider.Status(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	status.Compact = semanticCompactCapability(provider)
	return c.JSON(http.StatusOK, status)
}

// --- helpers ---

// resolveEnabledScopes returns bot-shared namespace scope.
func (*MemoryHandler) resolveEnabledScopes(botID string) ([]namespaceScope, error) {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "bot id is empty")
	}
	return []namespaceScope{{
		Namespace: sharedMemoryNamespace,
		ScopeID:   botID,
	}}, nil
}

// resolveWriteScope returns (scopeID, botID) for shared bot memory.
func (*MemoryHandler) resolveWriteScope(botID string) (string, string, error) {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return "", "", echo.NewHTTPError(http.StatusInternalServerError, "bot id is empty")
	}
	return botID, botID, nil
}

func normalizeSharedMemoryNamespace(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", sharedMemoryNamespace:
		return sharedMemoryNamespace, nil
	default:
		return "", echo.NewHTTPError(http.StatusBadRequest, "invalid namespace: "+raw)
	}
}

func (*MemoryHandler) resolveBotID(c echo.Context) (string, error) {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	return botID, nil
}

func buildNamespaceFilters(namespace, scopeID string, extra map[string]any) map[string]any {
	filters := map[string]any{
		"namespace": namespace,
		"scopeId":   scopeID,
	}
	for k, v := range extra {
		if k != "namespace" && k != "scopeId" {
			filters[k] = v
		}
	}
	return filters
}

func semanticCompactCapability(provider memprovider.Provider) memprovider.MemoryCompactCapability {
	if provider == nil {
		return memprovider.MemoryCompactCapability{Reason: "memory service not available"}
	}
	semanticProvider, ok := provider.(memprovider.SemanticCompactProvider)
	if !ok {
		return memprovider.MemoryCompactCapability{Reason: "selected memory provider does not support semantic compact"}
	}
	capability := semanticProvider.SemanticCompactCapability()
	if !capability.Semantic && strings.TrimSpace(capability.Reason) == "" {
		capability.Reason = "selected memory provider does not support semantic compact"
	}
	return capability
}

func deduplicateMemoryItems(botID string, items []memprovider.MemoryItem) []memprovider.MemoryItem {
	if len(items) == 0 {
		return items
	}
	seen := make(map[string]memprovider.MemoryItem, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		item = canonicalizeMemoryItem(botID, item)
		if item.ID == "" {
			continue
		}
		if existing, ok := seen[item.ID]; ok {
			if memoryItemRank(item) > memoryItemRank(existing) {
				seen[item.ID] = item
			}
			continue
		}
		seen[item.ID] = item
		order = append(order, item.ID)
	}
	result := make([]memprovider.MemoryItem, 0, len(order))
	for _, id := range order {
		result = append(result, seen[id])
	}
	return result
}

func canonicalizeMemoryItem(botID string, item memprovider.MemoryItem) memprovider.MemoryItem {
	item.ID = canonicalMemoryID(botID, item.ID)
	if strings.TrimSpace(item.BotID) == "" {
		item.BotID = strings.TrimSpace(botID)
	}
	return item
}

func canonicalMemoryID(botID, id string) string {
	botID = strings.TrimSpace(botID)
	localID := localMemoryID(id)
	if localID == "" {
		return ""
	}
	if botID == "" {
		return localID
	}
	return botID + ":" + localID
}

func localMemoryID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if idx := strings.Index(id, ":"); idx >= 0 && idx+1 < len(id) {
		return strings.TrimSpace(id[idx+1:])
	}
	return id
}

func memoryItemRank(item memprovider.MemoryItem) int {
	score := len(strings.TrimSpace(item.Memory))
	if strings.TrimSpace(item.Hash) != "" {
		score += 50
	}
	if strings.TrimSpace(item.CreatedAt) != "" {
		score += 10
	}
	if strings.TrimSpace(item.UpdatedAt) != "" {
		score += 10
	}
	if item.Metadata != nil {
		score += len(item.Metadata) * 2
		for _, key := range []string{"subject", "topic", "layer", "profile_ref", "confidence"} {
			if graphMetadataString(item.Metadata, key) != "" {
				score += 20
			}
		}
	}
	return score
}

func graphMemoryItemRank(item memprovider.MemoryItem, spec migrate.NodeSpec) int {
	score := memoryItemRank(item)
	if strings.TrimSpace(spec.Subject) != "" {
		score += 40
	}
	if strings.TrimSpace(spec.Topic) != "" {
		score += 20
	}
	if spec.Layer != "" {
		score += 10
	}
	return score
}

func (*MemoryHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}

func (h *MemoryHandler) requireBotAccess(c echo.Context) (string, error) {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID, err := h.resolveBotID(c)
	if err != nil {
		return "", err
	}
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID); err != nil {
		return "", err
	}
	return botID, nil
}
