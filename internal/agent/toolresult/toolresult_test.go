package toolresult

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Rows written before the formats existed hold {output, exitCode}; they read
// back as an empty result instead of an error.
func TestOldExecRowDecodesAsEmptyOutput(t *testing.T) {
	old := map[string]any{"output": "2026-10-24 01:24:46 CST\n", "exitCode": float64(0)}

	got, err := Decode[Exec](old)
	if err != nil {
		t.Fatalf("decode old row: %v", err)
	}
	if got != (Exec{}) {
		t.Fatalf("old row = %#v, want the zero Exec", got)
	}
	if _, err := DecodeStrict[Exec](old); err == nil {
		t.Fatal("strict decode accepted an old-shape row")
	}
}

// The wire JSON of the native records: always-written fields stay present when
// empty, optional ones disappear.
func TestRecordsKeepNativeFieldNames(t *testing.T) {
	cases := []struct {
		name string
		got  map[string]any
		want string
	}{
		{"exec", Exec{ExitCode: Ptr(0)}.Map(), `{"exit_code":0,"stderr":"","stdout":""}`},
		{"exec without exit code", Exec{}.Map(), `{"stderr":"","stdout":""}`},
		{"read", Read{Content: "x\n", TotalLines: 2}.Map(), `{"content":"x\n","total_lines":2}`},
		{"write", Write{OK: true}.Map(), `{"ok":true}`},
		{
			"exec started", ExecBackground{Status: ExecStatusBackgroundStarted, TaskID: "t", OutputFile: "/o", Message: "m"}.Map(),
			`{"message":"m","output_file":"/o","status":"background_started","task_id":"t"}`,
		},
		{"exec running task", ExecTask{OutputFile: "/o", OutputTail: "tail"}.Map(), `{"output_file":"/o","output_tail":"tail"}`},
		{"exec finished task", ExecTask{OutputFile: "/o", ExitCode: Ptr(0)}.Map(), `{"exit_code":0,"output_file":"/o","output_tail":""}`},
		{"search", WebSearch{Query: "q"}.Map(), `{"query":"q","results":[]}`},
		{"failure", NewFailure("no").Map(), `{"content":[{"text":"no","type":"text"}],"isError":true}`},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.got)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != tc.want {
			t.Errorf("%s = %s, want %s", tc.name, raw, tc.want)
		}
	}
}

func TestMergeKeepsAgentFieldsBesideTheRecord(t *testing.T) {
	got := Merge(Exec{Stdout: "ok"}, map[string]any{"stdout": "ignored", "truncated": true})
	if got["stdout"] != "ok" || got["truncated"] != true {
		t.Fatalf("merged = %#v", got)
	}
}

func TestNewApplyPatchListsAddedThenModifiedThenDeleted(t *testing.T) {
	got := NewApplyPatch([]string{"/a"}, []string{"/m1", "/m2"}, []string{"/d"})
	want := ApplyPatch{
		OK:       true,
		Summary:  "Success. Updated the following files:\nA /a\nM /m1\nM /m2\nD /d",
		Added:    []string{"/a"},
		Modified: []string{"/m1", "/m2"},
		Deleted:  []string{"/d"},
		Files: []ApplyPatchFile{
			{Operation: "add", Path: "/a"},
			{Operation: "modify", Path: "/m1"},
			{Operation: "modify", Path: "/m2"},
			{Operation: "delete", Path: "/d"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NewApplyPatch = %#v, want %#v", got, want)
	}
}
