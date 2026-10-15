package external

import "testing"

func TestProcessEnvMarksOnlySubagentThreads(t *testing.T) {
	if env := (PromptInput{ThreadID: "child", SessionMode: "subagent"}).ProcessEnv(); len(env) != 1 || env[0] != SubagentThreadEnv+"=child" {
		t.Fatalf("subagent env=%v", env)
	}
	for _, input := range []PromptInput{{ThreadID: "parent", SessionMode: "chat"}, {SessionMode: "subagent"}} {
		if env := input.ProcessEnv(); env != nil {
			t.Fatalf("input=%+v env=%v", input, env)
		}
	}
}
