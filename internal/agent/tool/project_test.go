package tools

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	projectpkg "github.com/memohai/memoh/internal/project"
)

const (
	testBotID     = "11111111-1111-1111-1111-111111111111"
	testProjectID = "22222222-2222-2222-2222-222222222222"
	testNodeID    = "33333333-3333-3333-3333-333333333333"
)

// fakeProjectService records what the tools asked for; only the methods a test
// exercises need meaningful behavior.
type fakeProjectService struct {
	projects []projectpkg.Project
	node     projectpkg.Node
	body     string

	lastActor   projectpkg.Actor
	lastContent projectpkg.UpdateContentRequest
	lastComment projectpkg.CommentRequest
	lastIssue   projectpkg.UpdateIssueRequest
}

func (f *fakeProjectService) ListProjects(context.Context) ([]projectpkg.Project, error) {
	return f.projects, nil
}

func (f *fakeProjectService) ResolveProject(_ context.Context, ref string) (projectpkg.Project, error) {
	for _, p := range f.projects {
		if p.Name == ref || p.ID == ref {
			return p, nil
		}
	}
	return projectpkg.Project{}, projectpkg.ErrProjectNotFound
}

func (f *fakeProjectService) ResolveNode(context.Context, string, string) (projectpkg.Node, error) {
	return f.node, nil
}

func (*fakeProjectService) DocPath(context.Context, string, string) (string, error) {
	return "需求文档/数据模型", nil
}

func (*fakeProjectService) Tree(context.Context, string) ([]projectpkg.TreeNode, error) {
	return nil, nil
}

func (*fakeProjectService) Board(context.Context, string) ([]projectpkg.Issue, error) {
	return nil, nil
}

func (*fakeProjectService) Search(context.Context, projectpkg.SearchRequest) ([]projectpkg.SearchResult, error) {
	return nil, nil
}

func (f *fakeProjectService) GetNode(context.Context, string, string) (projectpkg.NodeDetail, error) {
	node := f.node
	node.Body = f.body
	return projectpkg.NodeDetail{Node: node}, nil
}

func (*fakeProjectService) ListComments(context.Context, string, string) ([]projectpkg.Comment, error) {
	return nil, nil
}

func (f *fakeProjectService) CreateNode(_ context.Context, _ string, actor projectpkg.Actor, _ projectpkg.CreateNodeRequest) (projectpkg.NodeDetail, error) {
	f.lastActor = actor
	return projectpkg.NodeDetail{Node: f.node}, nil
}

func (f *fakeProjectService) UpdateContent(_ context.Context, _, _ string, actor projectpkg.Actor, req projectpkg.UpdateContentRequest) (projectpkg.Node, error) {
	f.lastActor = actor
	f.lastContent = req
	return f.node, nil
}

func (f *fakeProjectService) UpdateIssue(_ context.Context, _, _ string, actor projectpkg.Actor, req projectpkg.UpdateIssueRequest) (projectpkg.IssueDetails, error) {
	f.lastActor = actor
	f.lastIssue = req
	return projectpkg.IssueDetails{}, nil
}

func (f *fakeProjectService) CreateComment(_ context.Context, _, _ string, actor projectpkg.Actor, req projectpkg.CommentRequest) (projectpkg.Comment, error) {
	f.lastActor = actor
	f.lastComment = req
	return projectpkg.Comment{}, nil
}

func newFake() *fakeProjectService {
	return &fakeProjectService{
		projects: []projectpkg.Project{{ID: testProjectID, Name: "产品设计"}},
		node: projectpkg.Node{
			ID: testNodeID, ProjectID: testProjectID,
			Type: projectpkg.NodeTypeDoc, Title: "数据模型", Version: 7,
		},
		body: "# 表设计\n第一段\n第二段\n",
	}
}

func toolsByName(t *testing.T, provider *ProjectProvider, session SessionContext) map[string]sdk.Tool {
	t.Helper()
	list, err := provider.Tools(context.Background(), session)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	byName := make(map[string]sdk.Tool, len(list))
	for _, tool := range list {
		byName[tool.Name] = tool
	}
	return byName
}

func exec(t *testing.T, tool sdk.Tool, args map[string]any) (any, error) {
	t.Helper()
	return tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, args)
}

// The gate that keeps seven tools out of every prompt on deployments that
// never use Projects.
func TestProjectToolsUnregisteredWithoutProjects(t *testing.T) {
	fake := newFake()
	fake.projects = nil
	list, err := NewProjectProvider(nil, fake).Tools(context.Background(), SessionContext{BotID: testBotID})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected no tools without projects, got %d", len(list))
	}
}

func TestProjectToolsRegisteredWithProjects(t *testing.T) {
	byName := toolsByName(t, NewProjectProvider(nil, newFake()), SessionContext{BotID: testBotID})
	for _, want := range []string{
		ToolProjectList().String(), ToolProjectSearch().String(), ToolProjectRead().String(),
		ToolProjectCreate().String(), ToolProjectEdit().String(),
		ToolProjectIssueUpdate().String(), ToolProjectComment().String(),
	} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("tool %q not registered", want)
		}
	}
}

// Every write must land on the bot half of the author/editor column pair —
// that attribution is the whole reason those columns were reserved.
func TestProjectWritesAreAttributedToTheBot(t *testing.T) {
	fake := newFake()
	byName := toolsByName(t, NewProjectProvider(nil, fake), SessionContext{BotID: testBotID})

	if _, err := exec(t, byName[ToolProjectComment().String()], map[string]any{
		"project": "产品设计", "node": "需求文档/数据模型", "body": "来自 agent 的评论",
	}); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if fake.lastActor.BotID != testBotID || fake.lastActor.UserID != "" {
		t.Fatalf("comment actor = %+v, want bot %s", fake.lastActor, testBotID)
	}
	if fake.lastComment.Body != "来自 agent 的评论" {
		t.Fatalf("comment body = %q", fake.lastComment.Body)
	}

	if _, err := exec(t, byName[ToolProjectCreate().String()], map[string]any{
		"project": "产品设计", "type": "doc", "title": "新文档",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if fake.lastActor.BotID != testBotID {
		t.Fatalf("create actor = %+v, want bot %s", fake.lastActor, testBotID)
	}
}

func TestProjectEditRequiresExpectedVersion(t *testing.T) {
	byName := toolsByName(t, NewProjectProvider(nil, newFake()), SessionContext{BotID: testBotID})
	_, err := exec(t, byName[ToolProjectEdit().String()], map[string]any{
		"project": "产品设计", "node": "需求文档/数据模型",
		"old_string": "第一段", "new_string": "改过的第一段",
	})
	if err == nil || !strings.Contains(err.Error(), "expected_version") {
		t.Fatalf("expected an expected_version error, got %v", err)
	}
}

// A snippet that appears twice must be refused, not applied to whichever copy
// happens to come first — that is how an agent silently edits the wrong place.
func TestProjectEditRejectsAmbiguousSnippet(t *testing.T) {
	fake := newFake()
	fake.body = "重复\n中间\n重复\n"
	byName := toolsByName(t, NewProjectProvider(nil, fake), SessionContext{BotID: testBotID})

	_, err := exec(t, byName[ToolProjectEdit().String()], map[string]any{
		"project": "产品设计", "node": "需求文档/数据模型", "expected_version": 7,
		"old_string": "重复", "new_string": "改过",
	})
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("expected an ambiguity error, got %v", err)
	}
}

func TestProjectEditRejectsMissingSnippet(t *testing.T) {
	byName := toolsByName(t, NewProjectProvider(nil, newFake()), SessionContext{BotID: testBotID})
	_, err := exec(t, byName[ToolProjectEdit().String()], map[string]any{
		"project": "产品设计", "node": "需求文档/数据模型", "expected_version": 7,
		"old_string": "不存在的文字", "new_string": "x",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

func TestProjectEditAppliesSnippetReplacement(t *testing.T) {
	fake := newFake()
	byName := toolsByName(t, NewProjectProvider(nil, fake), SessionContext{BotID: testBotID})

	if _, err := exec(t, byName[ToolProjectEdit().String()], map[string]any{
		"project": "产品设计", "node": "需求文档/数据模型", "expected_version": 7,
		"old_string": "第一段", "new_string": "改过的第一段",
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if fake.lastContent.Body == nil {
		t.Fatal("edit did not send a body")
	}
	if got := *fake.lastContent.Body; got != "# 表设计\n改过的第一段\n第二段\n" {
		t.Fatalf("body = %q", got)
	}
	if fake.lastContent.ExpectedVersion != 7 {
		t.Fatalf("expected_version = %d, want 7", fake.lastContent.ExpectedVersion)
	}
}

// The doc/issue split is the one distinction the model is most likely to get
// wrong, so the wrong tool must say so rather than half-work.
func TestProjectIssueUpdateRejectsDocs(t *testing.T) {
	byName := toolsByName(t, NewProjectProvider(nil, newFake()), SessionContext{BotID: testBotID})
	_, err := exec(t, byName[ToolProjectIssueUpdate().String()], map[string]any{
		"project": "产品设计", "node": "需求文档/数据模型",
		"expected_revision": 1, "status": "done",
	})
	if err == nil || !strings.Contains(err.Error(), "doc") {
		t.Fatalf("expected a doc-vs-issue error, got %v", err)
	}
}

func TestProjectIssueUpdateClearsPriority(t *testing.T) {
	fake := newFake()
	fake.node.Type = projectpkg.NodeTypeIssue
	fake.node.Number = 12
	byName := toolsByName(t, NewProjectProvider(nil, fake), SessionContext{BotID: testBotID})

	if _, err := exec(t, byName[ToolProjectIssueUpdate().String()], map[string]any{
		"project": "产品设计", "node": "#12",
		"expected_revision": 3, "priority": "none",
	}); err != nil {
		t.Fatalf("issue update: %v", err)
	}
	if fake.lastIssue.Priority == nil || *fake.lastIssue.Priority != "" {
		t.Fatalf("priority = %v, want cleared to empty", fake.lastIssue.Priority)
	}
	if fake.lastIssue.ExpectedRevision != 3 {
		t.Fatalf("expected_revision = %d, want 3", fake.lastIssue.ExpectedRevision)
	}
}

// Usage guidance must be gated on what is actually registered: it is injected
// into the prompt, and naming a tool that is not there teaches the model to
// call something that does not exist.
func TestProjectUsageGatesRegisteredTools(t *testing.T) {
	provider := NewProjectProvider(nil, newFake())
	session := SessionContext{BotID: testBotID}

	if got := provider.Usage(context.Background(), session, AvailableTools{}); got != "" {
		t.Fatalf("Usage without available tools = %q, want empty", got)
	}

	// project_read is the anchor of the read-then-write contract; without it
	// there is no workflow to describe.
	got := provider.Usage(context.Background(), session, availableToolsForTest(ToolProjectRead()))
	if !strings.Contains(got, "`project_read`") {
		t.Fatalf("Usage should mention project_read, got:\n%s", got)
	}
	if strings.Contains(got, "`project_edit`") {
		t.Fatalf("Usage without project_edit must not mention it, got:\n%s", got)
	}

	got = provider.Usage(context.Background(), session,
		availableToolsForTest(ToolProjectRead(), ToolProjectEdit(), ToolProjectIssueUpdate()))
	for _, want := range []string{"`project_edit`", "expected_version", "`project_issue_update`", "expected_revision"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Usage is missing %q, got:\n%s", want, got)
		}
	}
	// The injection warning must ride along with any read capability.
	if !strings.Contains(got, "DATA") {
		t.Fatalf("Usage must warn that project content is data, not instructions, got:\n%s", got)
	}
}

// Shared content must arrive labelled, so a "ignore your instructions" line
// inside a doc reads as data rather than as a command.
func TestProjectReadMarksBodyAsUntrusted(t *testing.T) {
	byName := toolsByName(t, NewProjectProvider(nil, newFake()), SessionContext{BotID: testBotID})
	out, err := exec(t, byName[ToolProjectRead().String()], map[string]any{
		"project": "产品设计", "node": "需求文档/数据模型",
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	result, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", out)
	}
	if result["body_is_untrusted_content"] != true {
		t.Fatalf("read result is missing the untrusted-content marker: %+v", result)
	}
	if result["version"] != 7 {
		t.Fatalf("version = %v, want 7 (the value edit requires)", result["version"])
	}
}
