package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/discuss"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/contextview"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/settings"
)

// summaryLimitPattern reads the summary bound the compaction prompt states.
var summaryLimitPattern = regexp.MustCompile(`must stay under (\d+) tokens`)

type recordingFailureBroadcaster struct {
	failures chan string
}

func (b *recordingFailureBroadcaster) PublishEvent(_ string, event channel.StreamEvent) {
	if event.Type == channel.StreamEventError {
		b.failures <- event.ErrorCode
	}
}

// backlogFixture drives the real discuss driver against the real application
// service, PostgreSQL and a scripted provider: an 8000-token chat model behind
// a 16000-token Channel admission cap.
type backlogFixture struct {
	t           *testing.T
	ctx         context.Context
	pool        *pgxpool.Pool
	service     *application.Service
	botID       string
	sessionID   string
	chatModel   string
	providerURL string
	rc          timeline.RenderedContext
	base        time.Time
	messages    messagepkg.Service
	driver      *discuss.DiscussDriver
	cfg         discuss.DiscussSessionConfig
	cursor      *notifyingRecoveryCursor
	broadcaster *recordingFailureBroadcaster
	images      *backlogImageLoader
	summaries   atomic.Int32
	failSummary atomic.Bool
	// failStatus is the status a failing summarizer answers with; zero is a
	// rejected request, which is not retried.
	failStatus  atomic.Int32
	longSummary atomic.Bool
	onSummary   func(body []byte)
	modelCalls  atomic.Int32
	lastRequest atomic.Value
	// stream answers chat requests when set; the default answers at once.
	stream      func(http.ResponseWriter, []byte)
	agent       *native.Agent
	manager     *sessionruntime.Manager
	ownerUserID string
}

func newBacklogFixture(t *testing.T) *backlogFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	f := &backlogFixture{t: t, ctx: ctx}
	f.pool = openDiscussRecoveryPostgres(t, ctx)
	f.botID, f.sessionID = createDiscussRecoveryFixture(t, ctx, f.pool)
	queries := postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
	var teamID string
	if err := f.pool.QueryRow(ctx, `SELECT team_id, owner_user_id FROM bots WHERE id=$1`, f.botID).Scan(&teamID, &f.ownerUserID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
			return
		}
		if request.Stream {
			f.modelCalls.Add(1)
			f.lastRequest.Store(string(body))
			if f.stream != nil {
				f.stream(w, body)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"reply\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"BACKLOG_OK\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"reply\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		f.summaries.Add(1)
		if f.onSummary != nil {
			f.onSummary(body)
		}
		if f.failSummary.Load() {
			http.Error(w, "summarizer down", cmp.Or(int(f.failStatus.Load()), http.StatusBadRequest))
			return
		}
		summary := "Earlier backlog summarized."
		if stated := summaryLimitPattern.FindSubmatch(body); stated != nil && f.longSummary.Load() {
			limit, _ := strconv.Atoi(string(stated[1]))
			summary += strings.Repeat(" kept", limit*4/5-len(summary)/5)
		}
		content, _ := json.Marshal(summary)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"summary","object":"chat.completion","model":"compact-model","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}]}`, content)
	}))
	t.Cleanup(server.Close)
	f.providerURL = server.URL
	chatModel, providerID := uuid.NewString(), uuid.NewString()
	f.chatModel = chatModel
	providerConfig, _ := json.Marshal(map[string]string{"base_url": server.URL, "api_key": "test-key"})
	if _, err := f.pool.Exec(ctx, `INSERT INTO providers(id,name,client_type,config) VALUES($1,$2,'openai-completions',$3)`, providerID, providerID, providerConfig); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM providers WHERE id=$1`, providerID) })
	config, _ := json.Marshal(map[string]any{"context_window": 8000, "compatibilities": []string{}})
	if _, err := f.pool.Exec(ctx, `INSERT INTO models(id,model_id,provider_id,config) VALUES($1,'chat-model',$2,$3)`, chatModel, providerID, config); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE bots SET chat_model_id=$2 WHERE id=$1`, f.botID, chatModel); err != nil {
		t.Fatal(err)
	}
	logger := slog.Default()
	f.messages = messagepkg.NewService(logger, queries)
	f.base = time.Now().Add(time.Minute).Truncate(time.Millisecond)
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{OwnerID: uuid.NewString(), Ledger: ledger.NewPostgres(sqlc.New(f.pool), f.pool), Fence: runtimefence.NewActivator(queries)})
	t.Cleanup(func() { _ = manager.Close() })
	f.manager = manager
	f.agent = native.New(native.Deps{Logger: logger, ContextViewApplier: contextview.ProviderRunConfigApplier(logger)})
	accountStore, err := postgresstore.New(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewService(logger, models.NewService(logger, queries), queries, f.messages, settings.NewService(logger, queries, nil, nil), accounts.NewService(logger, accountStore), f.agent, time.UTC, time.Minute)
	service.SetSessionRuntime(manager)
	service.SetCompactionService(compaction.NewService(logger, queries))
	service.SetContextAbsoluteMaxTokens(16000)
	f.service = service
	f.cursor = &notifyingRecoveryCursor{EventStore: timeline.NewEventStore(logger, queries), advanced: make(chan timeline.DiscussCursorPosition, 4)}
	f.broadcaster = &recordingFailureBroadcaster{failures: make(chan string, 16)}
	f.driver = discuss.NewDiscussDriver(discuss.DiscussDriverDeps{Turn: service, CursorStore: f.cursor, MessageService: f.messages, Artifacts: compaction.NewTimelineArtifactSource(queries), Broadcaster: f.broadcaster, AdmissionMaxTokens: 16000, Logger: logger})
	t.Cleanup(f.driver.StopAll)
	f.cfg = discuss.DiscussSessionConfig{BotID: f.botID, ThreadID: f.sessionID, TeamID: teamID}
	return f
}

func (f *backlogFixture) appendMessage(id, text string) {
	f.t.Helper()
	f.appendMessageAt(id, text, f.base.Add(time.Duration(len(f.rc)+1)*time.Second))
}

// appendMessageAt delivers text as received at created.
func (f *backlogFixture) appendMessageAt(id, text string, created time.Time) {
	f.t.Helper()
	segment, err := f.deliver(f.ctx, id, text, created)
	if err != nil {
		f.t.Fatal(err)
	}
	f.rc = append(f.rc, segment)
}

// deliver writes text as the message ingress stores, received at created,
// and returns the segment the timeline renders for it. It does not touch the
// fixture's rendered context, so a tool may call it while a run reads it.
func (f *backlogFixture) deliver(ctx context.Context, id, text string, created time.Time) (timeline.RenderedSegment, error) {
	eventID, event, err := f.cursor.PersistEvent(ctx, f.botID, f.sessionID, timeline.MessageEvent{SessionID: f.sessionID, MessageID: id, ReceivedAtMs: created.UnixMilli(), Content: []timeline.ContentNode{{Type: "text", Text: text}}})
	if err != nil {
		return timeline.RenderedSegment{}, err
	}
	content, _ := json.Marshal(text)
	msg, err := f.messages.Persist(ctx, messagepkg.PersistInput{BotID: f.botID, SessionID: f.sessionID, ExternalMessageID: id, EventID: eventID, Role: "user", Content: content, SessionMode: "discuss"})
	if err != nil {
		return timeline.RenderedSegment{}, err
	}
	if _, err := f.pool.Exec(ctx, `UPDATE bot_history_messages SET created_at=$2 WHERE id=$1`, msg.ID, created); err != nil {
		return timeline.RenderedSegment{}, err
	}
	return timeline.RenderedSegment{MessageID: id, LastEventCursor: event.(timeline.MessageEvent).EventCursor, ReceivedAtMs: created.UnixMilli(), Content: []timeline.RenderedContentPiece{{Type: "text", Text: text}}}, nil
}

// restartDriver replaces the Discuss driver with a fresh one, the way a
// worker comes back after a restart or an idle eviction: it knows only what
// the cursor store and the history hold.
func (f *backlogFixture) restartDriver() {
	f.driver.StopAll()
	f.driver = discuss.NewDiscussDriver(discuss.DiscussDriverDeps{Turn: f.service, CursorStore: f.cursor, MessageService: f.messages, Artifacts: compaction.NewTimelineArtifactSource(postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))), Broadcaster: f.broadcaster, AdmissionMaxTokens: 16000, Logger: slog.Default()})
	f.t.Cleanup(f.driver.StopAll)
}

// trigger notifies the driver and reports the failure code, or "" when the
// batch was answered and consumed.
func (f *backlogFixture) trigger() string {
	f.t.Helper()
	f.driver.NotifyRC(f.ctx, f.sessionID, f.rc, f.cfg)
	select {
	case position := <-f.cursor.advanced:
		if position != timeline.ConsumedDiscussCursor(f.rc) {
			f.t.Fatalf("cursor did not consume the batch: %+v", position)
		}
		return ""
	case code := <-f.broadcaster.failures:
		return code
	case <-f.ctx.Done():
		f.t.Fatal("trigger did not finish")
		return ""
	}
}

// answer asserts one trigger ends answered by exactly one provider call that
// carries the newest input, and returns the provider request.
func (f *backlogFixture) answer(round, wantText string) string {
	f.t.Helper()
	callsBefore := f.modelCalls.Load()
	if code := f.trigger(); code != "" {
		f.t.Fatalf("%s: trigger failed with %s; summary_calls=%d provider_calls=%d", round, code, f.summaries.Load(), f.modelCalls.Load())
	}
	request, _ := f.lastRequest.Load().(string)
	if f.modelCalls.Load()-callsBefore != 1 || strings.Count(request, wantText) != 1 {
		f.t.Fatalf("%s: provider calls=%d, newest input multiplicity=%d", round, f.modelCalls.Load()-callsBefore, strings.Count(request, wantText))
	}
	return request
}

func (f *backlogFixture) appendBacklog(count, size int) {
	for i := range count {
		f.appendMessage(fmt.Sprintf("backlog-%d", i), fmt.Sprintf("backlog-%d ", i)+strings.Repeat("x", size))
	}
}

func (f *backlogFixture) backlogRows() (raw, compacted int) {
	return f.backlogRowsPrefixed("backlog-")
}

func (f *backlogFixture) backlogRowsPrefixed(prefix string) (raw, compacted int) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE c.status IS DISTINCT FROM 'ok'), count(*) FILTER (WHERE c.status = 'ok') FROM bot_history_messages h LEFT JOIN bot_history_message_compacts c ON c.id=h.compact_id WHERE h.session_id=$1 AND h.source_message_id LIKE $2`, f.sessionID, prefix+"%").Scan(&raw, &compacted); err != nil {
		f.t.Fatal(err)
	}
	return raw, compacted
}

// omittedImages collects the inputs whose images runs recorded as shed.
func (f *backlogFixture) omittedImages() map[string]bool {
	f.t.Helper()
	omitted := map[string]bool{}
	rows, err := f.pool.Query(f.ctx, `SELECT coalesce(snapshot->'mutations','[]'::jsonb)::text FROM context_lifecycles WHERE session_id=$1`, f.sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var mutations string
		if err := rows.Scan(&mutations); err != nil {
			f.t.Fatal(err)
		}
		var records []struct{ Kind, Detail string }
		_ = json.Unmarshal([]byte(mutations), &records)
		for _, record := range records {
			if record.Kind == "current_input_images_omitted" {
				for _, id := range strings.Split(strings.TrimPrefix(record.Detail, "sources="), ",") {
					omitted[id] = true
				}
			}
		}
	}
	return omitted
}

// A backlog of unconsumed input larger than the model budget must not stall
// the session: the trigger is answered after bounded recovery that summarizes
// the older input and keeps the newest fitting suffix raw, and the next small
// message is answered too.
func TestPostgresDiscussBacklogOverBudgetIsAnsweredAndNextRoundContinues(t *testing.T) {
	f := newBacklogFixture(t)
	f.appendBacklog(12, 2800)
	request := f.answer("backlog", "backlog-11 ")
	if f.summaries.Load() < 1 || f.summaries.Load() > 3 || !strings.Contains(request, "Earlier backlog summarized.") || strings.Contains(request, "backlog-0 ") {
		t.Fatalf("older input must be summarized within the recovery bound: summaries=%d", f.summaries.Load())
	}
	raw, compacted := f.backlogRows()
	if compacted == 0 || raw == 0 {
		t.Fatalf("backlog must be summarized behind a raw newest suffix: raw=%d compacted=%d", raw, compacted)
	}
	summaries := f.summaries.Load()
	f.appendMessage("next-round", "next-round small message")
	f.answer("next round", "next-round small message")
	if f.summaries.Load() != summaries {
		t.Fatalf("the round after recovery needed %d more summaries", f.summaries.Load()-summaries)
	}
	t.Logf("summary_calls=%d provider_calls=%d backlog_raw=%d backlog_compacted=%d", f.summaries.Load(), f.modelCalls.Load(), raw, compacted)
}

// A backlog the summarizer cannot reduce fails explicitly without a provider
// call or consuming the batch; once the summarizer is back, the next message
// is answered.
func TestPostgresDiscussBacklogSummarizerFailureFailsExplicitly(t *testing.T) {
	f := newBacklogFixture(t)
	f.failSummary.Store(true)
	f.appendBacklog(12, 2800)
	if code := f.trigger(); code != "context.budget_unsatisfied" || f.modelCalls.Load() != 0 {
		t.Fatalf("code=%q provider_calls=%d, want budget_unsatisfied before any provider call", code, f.modelCalls.Load())
	}
	if raw, compacted := f.backlogRows(); raw != 12 || compacted != 0 {
		t.Fatalf("failed summaries changed history: raw=%d compacted=%d", raw, compacted)
	}
	f.failSummary.Store(false)
	f.appendMessage("next-round", "next-round small message")
	request := f.answer("next round", "next-round small message")
	if !strings.Contains(request, "Earlier backlog summarized.") {
		t.Fatal("the recovered round did not summarize the backlog")
	}
}

// A newest input that cannot fit even alone fails once, without consuming the
// batch. Demoted to history, it is still more than the summarizer can read in
// one call, so the next round fails explicitly instead of answering without it.
func TestPostgresDiscussUnreadableOversizedInputFailsExplicitly(t *testing.T) {
	for _, size := range []int{70_000, 600_000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			f := newBacklogFixture(t)
			f.appendMessage("oversized", "oversized "+strings.Repeat("y", size))
			if code := f.trigger(); code != "context.protected_overflow" || f.modelCalls.Load() != 0 {
				t.Fatalf("oversized newest input: code=%q provider_calls=%d, want one bounded protected_overflow", code, f.modelCalls.Load())
			}
			f.appendMessage("next-round", "next-round small message")
			if code := f.trigger(); code != "context.budget_unsatisfied" || f.modelCalls.Load() != 0 || f.summaries.Load() != 0 {
				t.Fatalf("next round: code=%q provider_calls=%d summaries=%d, want budget_unsatisfied before any call", code, f.modelCalls.Load(), f.summaries.Load())
			}
			var compacted bool
			if err := f.pool.QueryRow(f.ctx, `SELECT compact_id IS NOT NULL FROM bot_history_messages WHERE session_id=$1 AND source_message_id='oversized'`, f.sessionID).Scan(&compacted); err != nil {
				t.Fatal(err)
			}
			if compacted {
				t.Fatal("an input the summarizer could not read was marked compacted")
			}
		})
	}
}

// backlogImageLoader serves each content hash as a PNG of its own width, so
// the provider request tells which stored image every image part is.
type backlogImageLoader struct {
	mu     sync.Mutex
	hashes map[string]string
}

func (l *backlogImageLoader) OpenForGateway(_ context.Context, _, contentHash string) (io.ReadCloser, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewGray(image.Rect(0, 0, len(l.hashes)+1, 1))); err != nil {
		return nil, "", err
	}
	l.hashes[encoded.String()] = contentHash
	return io.NopCloser(bytes.NewReader(encoded.Bytes())), "image/png", nil
}

func (*backlogImageLoader) AccessPathForGateway(context.Context, string, string) (string, error) {
	return "", io.EOF
}

func (l *backlogImageLoader) hash(data []byte) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hashes[string(data)]
}

// enableVision lets the chat model take images from a loader that tells the
// stored images apart.
func (f *backlogFixture) enableVision() {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, `UPDATE models SET config = config || '{"compatibilities":["vision"]}'::jsonb WHERE id=$1`, f.chatModel); err != nil {
		f.t.Fatal(err)
	}
	f.images = &backlogImageLoader{hashes: map[string]string{}}
	f.service.SetGatewayAssetLoader(f.images)
}

func (f *backlogFixture) appendImageMessage(id, text string, hashes ...string) {
	f.t.Helper()
	f.appendMessage(id, text)
	for _, hash := range hashes {
		f.rc[len(f.rc)-1].ImageRefs = append(f.rc[len(f.rc)-1].ImageRefs, timeline.ImageAttachmentRef{ContentHash: hash, Mime: "image/png"})
	}
}

// requestImages maps each provider message carrying images to the image
// content hashes it carries, keyed by the message's text.
func (f *backlogFixture) requestImages(request string) map[string][]string {
	t := f.t
	t.Helper()
	var body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(request), &body); err != nil {
		t.Fatal(err)
	}
	images := map[string][]string{}
	for _, message := range body.Messages {
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if json.Unmarshal(message.Content, &parts) != nil {
			continue
		}
		var text strings.Builder
		var hashes []string
		for _, part := range parts {
			switch part.Type {
			case "text":
				text.WriteString(part.Text)
			case "image_url":
				_, encoded, _ := strings.Cut(part.ImageURL.URL, ";base64,")
				decoded, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					t.Fatalf("image part %q: %v", part.ImageURL.URL, err)
				}
				hashes = append(hashes, f.images.hash(decoded))
			}
		}
		if len(hashes) > 0 {
			images[text.String()] = hashes
		}
	}
	return images
}

// assertImagesOnOwnMessages fails unless every image the request carries sits
// on the message it arrived with.
func (f *backlogFixture) assertImagesOnOwnMessages(request string, owners map[string]string) int {
	t := f.t
	t.Helper()
	total := 0
	for text, hashes := range f.requestImages(request) {
		for _, hash := range hashes {
			total++
			if owner, ok := owners[hash]; !ok || !strings.Contains(text, owner) {
				t.Fatalf("image %s arrived with %q but rides on a message %q", hash, owner, text)
			}
		}
	}
	return total
}

// Images of an unconsumed backlog belong to their own messages: the older
// ones are history recovery may compact, not protected input riding on the
// newest message. A backlog whose images exceed the window is answered, and
// the next message is answered too.
func TestPostgresDiscussImageBacklogKeepsImagesOnTheirMessages(t *testing.T) {
	f := newBacklogFixture(t)
	f.enableVision()
	owners := map[string]string{}
	for i := range 6 {
		id, hash := fmt.Sprintf("image-%d", i), fmt.Sprintf("image-hash-%d", i)
		owners[hash] = id + " "
		f.appendImageMessage(id, id+" look at this", hash)
	}
	request := f.answer("image backlog", "image-5 ")
	if f.assertImagesOnOwnMessages(request, owners) == 0 || len(f.requestImages(request)["image-5 look at this"]) != 1 {
		t.Fatalf("the newest input lost its image: %v", f.requestImages(request))
	}
	images, shed := f.requestImages(request), f.omittedImages()
	for i := range 6 {
		id := fmt.Sprintf("image-%d", i)
		if !strings.Contains(request, id+" look at this") {
			t.Fatalf("%s lost its text to image pressure", id)
		}
		if len(images[id+" look at this"]) == 0 && !shed[id] {
			t.Fatalf("%s lost its image without a record; shed=%v", id, shed)
		}
	}
	if len(images) < 2 {
		t.Fatalf("shedding must stop once the history fits, but only %v kept images", images)
	}
	f.appendMessage("next-round", "next-round small message")
	request = f.answer("next round", "next-round small message")
	f.assertImagesOnOwnMessages(request, owners)
	t.Logf("summary_calls=%d provider_calls=%d", f.summaries.Load(), f.modelCalls.Load())
}

// A newest message whose own images cannot fit fails once without consuming
// the batch; the next message demotes it to history and is answered without
// the demoted images riding on it.
func TestPostgresDiscussOversizedImageInputFailsOnceThenNextMessageIsAnswered(t *testing.T) {
	f := newBacklogFixture(t)
	f.enableVision()
	owners := map[string]string{}
	hashes := make([]string, 6)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("album-hash-%d", i)
		owners[hashes[i]] = "album "
	}
	f.appendImageMessage("album", "album of six", hashes...)
	if code := f.trigger(); code != "context.budget_unsatisfied" || f.modelCalls.Load() != 0 {
		t.Fatalf("oversized image input: code=%q provider_calls=%d, want one bounded failure", code, f.modelCalls.Load())
	}
	f.appendMessage("next-round", "next-round small message")
	request := f.answer("next round", "next-round small message")
	f.assertImagesOnOwnMessages(request, owners)
	if !strings.Contains(request, "album of six") {
		t.Fatal("the demoted album lost its text to its images")
	}
	if len(f.requestImages(request)["album of six"]) < len(hashes) && !f.omittedImages()["album"] {
		t.Fatal("the demoted album shed images without a record")
	}
	t.Logf("summary_calls=%d provider_calls=%d", f.summaries.Load(), f.modelCalls.Load())
}

// Answered history goes before the images of unconsumed input: an older
// unconsumed message keeps its image when summarizing what was already
// answered makes room.
func TestPostgresDiscussImageBacklogYieldsAnsweredHistoryFirst(t *testing.T) {
	for _, rounds := range []int{10, 4} {
		t.Run(fmt.Sprintf("rounds=%d", rounds), func(t *testing.T) {
			f := newBacklogFixture(t)
			f.enableVision()
			for i := range rounds {
				id := fmt.Sprintf("old-%d", i)
				f.appendMessage(id, id+" "+strings.Repeat("h", 2400))
				f.answer(id, id+" ")
			}
			f.appendImageMessage("img-old", "img-old look at this", "img-hash")
			f.appendMessage("newest", "newest small message")
			request := f.answer("batch", "newest small message")
			if len(f.requestImages(request)["img-old look at this"]) != 1 || f.omittedImages()["img-old"] {
				t.Fatalf("img-old's image was shed although answered history could make room; shed=%v", f.omittedImages())
			}
		})
	}
}

// Older unconsumed input that compaction summarizes cannot carry its images
// into the summary; each such image is still recorded, never silently lost.
func TestPostgresDiscussCompactedImageInputIsRecorded(t *testing.T) {
	f := newBacklogFixture(t)
	f.enableVision()
	for i := range 10 {
		id := fmt.Sprintf("img-%d", i)
		f.appendImageMessage(id, id+" look "+strings.Repeat("z", 2800), fmt.Sprintf("hash-%d", i))
	}
	request := f.answer("batch", "img-9 look")
	images, shed := f.requestImages(request), f.omittedImages()
	if _, compacted := f.backlogRowsPrefixed("img-"); compacted == 0 {
		t.Fatal("the fixture must summarize part of the batch")
	}
	for i := range 10 {
		id := fmt.Sprintf("img-%d", i)
		if len(images[id+" look "+strings.Repeat("z", 2800)]) == 0 && !shed[id] {
			t.Fatalf("%s: image neither delivered nor recorded; shed=%v", id, shed)
		}
	}
}

// Recovery that compacts keeps older input of the batch raw by the cost of its
// text, so an older message is not summarized for its image while newer ones
// keep theirs: images go before text, oldest first.
func TestPostgresDiscussCompactionKeepsBatchTextOverImages(t *testing.T) {
	for _, tc := range []struct{ rounds, images, size int }{{2, 3, 1200}, {4, 3, 1200}, {2, 5, 600}} {
		t.Run(fmt.Sprintf("rounds=%d/images=%d/size=%d", tc.rounds, tc.images, tc.size), func(t *testing.T) {
			f := newBacklogFixture(t)
			f.enableVision()
			for i := range tc.rounds {
				id := fmt.Sprintf("old-%d", i)
				f.appendMessage(id, id+" "+strings.Repeat("h", 2400))
				f.answer(id, id+" ")
			}
			for i := range tc.images {
				id := fmt.Sprintf("img-%d", i)
				f.appendImageMessage(id, id+" look "+strings.Repeat("t", tc.size), fmt.Sprintf("hash-%d", i))
			}
			f.appendMessage("newest", "newest small message")
			request := f.answer("batch", "newest small message")
			if _, compacted := f.backlogRowsPrefixed("img-"); compacted != 0 {
				t.Fatalf("%d batch messages were summarized for their images", compacted)
			}
			images, shed := f.requestImages(request), f.omittedImages()
			for i := range tc.images {
				id := fmt.Sprintf("img-%d", i)
				if !strings.Contains(request, id+" look ") {
					t.Fatalf("%s lost its text", id)
				}
				if len(images[id+" look "+strings.Repeat("t", tc.size)]) == 0 && !shed[id] {
					t.Fatalf("%s: image neither delivered nor recorded; shed=%v", id, shed)
				}
			}
		})
	}
}
