package schedule

import (
	"context"
	"errors"
	"testing"
)

func TestInvalidRequestErrorNamesFieldAndKeepsMessage(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		msg      string
		field    string
		required bool
	}{
		{"required sentinel", ErrTargetSessionRequired, "target_session_id is required for existing_session run target", "target_session_id", true},
		{"model required", ErrModelRequired, "this bot has no default model, so a scheduled run needs an explicit model", "model_id", true},
		{"invalid sentinel", ErrTargetSessionNotFound, "target session not found", "target_session_id", false},
		{"formatted", invalidFieldf("run_target", "unknown run_target %q", "x"), `unknown run_target "x"`, "run_target", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got InvalidRequestError
			if !errors.As(tc.err, &got) {
				t.Fatalf("%v is not an InvalidRequestError", tc.err)
			}
			if got.Error() != tc.msg || got.Field() != tc.field || got.Required() != tc.required {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", got.Error(), got.Field(), got.Required(), tc.msg, tc.field, tc.required)
			}
		})
	}
}

func TestCreateNamesTheMissingField(t *testing.T) {
	svc := &Service{queries: &executionQueries{}}
	for _, tc := range []struct {
		req   CreateRequest
		field string
	}{
		{CreateRequest{Pattern: "* * * * *", Command: "c"}, "name"},
		{CreateRequest{Name: "n", Command: "c"}, "pattern"},
		{CreateRequest{Name: "n", Pattern: "* * * * *"}, "command"},
	} {
		_, err := svc.Create(context.Background(), execTestBotID, tc.req)
		var got InvalidRequestError
		if !errors.As(err, &got) || got.Field() != tc.field || !got.Required() {
			t.Fatalf("Create(%+v) error = %v, want required %s", tc.req, err, tc.field)
		}
	}
}
