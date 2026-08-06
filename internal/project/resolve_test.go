package project

import (
	"errors"
	"strings"
	"testing"
)

func TestIsUUID(t *testing.T) {
	if !isUUID(uuidA) {
		t.Fatalf("expected %q to parse as a UUID", uuidA)
	}
	for _, ref := range []string{"", "产品设计", "#12", "需求文档/数据模型", "not-a-uuid"} {
		if isUUID(ref) {
			t.Fatalf("expected %q not to parse as a UUID", ref)
		}
	}
}

func TestAmbiguousErrorNamesCandidates(t *testing.T) {
	err := &AmbiguousError{
		Ref:  "数据模型",
		Kind: "node",
		Candidates: []Candidate{
			{ID: uuidA, Title: "数据模型", Path: "需求文档/数据模型"},
			{ID: uuidB, Title: "数据模型", Path: "会议记录/数据模型"},
		},
	}
	msg := err.Error()
	// The message is the agent's only way to disambiguate, so it must carry
	// both the human path and the id it should retry with.
	for _, want := range []string{"需求文档/数据模型", "会议记录/数据模型", uuidA, uuidB} {
		if !contains(msg, want) {
			t.Fatalf("error message %q is missing %q", msg, want)
		}
	}
	var target *AmbiguousError
	if !errors.As(error(err), &target) {
		t.Fatal("AmbiguousError must be recoverable with errors.As")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestDocPathsFromTree(t *testing.T) {
	// Same shape the tools use to render a doc's addressable path.
	tree := []TreeNode{
		{ID: "root", Title: "需求文档"},
		{ID: "child", ParentID: "root", Title: "数据模型"},
		{ID: "grandchild", ParentID: "child", Title: "表设计"},
		{ID: "other", Title: "会议记录"},
	}
	paths := treePaths(tree)
	cases := map[string]string{
		"root":       "需求文档",
		"child":      "需求文档/数据模型",
		"grandchild": "需求文档/数据模型/表设计",
		"other":      "会议记录",
	}
	for id, want := range cases {
		if paths[id] != want {
			t.Fatalf("path of %s = %q, want %q", id, paths[id], want)
		}
	}
}

// treePaths mirrors the tool-side path builder so the resolver and the
// renderer are proven to agree on the format ResolveNode reads back.
func treePaths(tree []TreeNode) map[string]string {
	titleOf := make(map[string]string, len(tree))
	parentOf := make(map[string]string, len(tree))
	for _, node := range tree {
		titleOf[node.ID] = node.Title
		parentOf[node.ID] = node.ParentID
	}
	paths := make(map[string]string, len(tree))
	for _, node := range tree {
		segments := []string{}
		for cursor, depth := node.ID, 0; cursor != "" && depth < 64; depth++ {
			title, ok := titleOf[cursor]
			if !ok {
				break
			}
			segments = append([]string{title}, segments...)
			cursor = parentOf[cursor]
		}
		paths[node.ID] = strings.Join(segments, "/")
	}
	return paths
}
