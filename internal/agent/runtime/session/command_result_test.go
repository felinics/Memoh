package sessionruntime

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
)

// A catalogued failure crosses to the routing process as its code and catalog
// args; the private text of the failure does not.
func TestCommandResultRestoresCataloguedFailure(t *testing.T) {
	failure := apperror.Wrap(apperror.CodeACPCommandNotFound, errors.New("private install detail"),
		map[string]string{"command": "python", "secret": "token"})
	raw, err := json.Marshal(newCommandResult(Command{ID: "command-not-found"}, failure))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private install detail") || strings.Contains(string(raw), "token") {
		t.Fatalf("command result %s carries private failure text", raw)
	}
	var result Command
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	got := commandResultError(result)
	if code := apperror.CodeOf(got); code != apperror.CodeACPCommandNotFound {
		t.Fatalf("restored code = %q, want %q (error %v)", code, apperror.CodeACPCommandNotFound, got)
	}
	if args, want := apperror.ArgsOf(got), map[string]string{"command": "python"}; !maps.Equal(args, want) {
		t.Fatalf("restored args = %v, want %v", args, want)
	}
}

func TestCommandResultKeepsSentinelFailures(t *testing.T) {
	got := commandResultError(newCommandResult(Command{ID: "command-busy"}, ErrCommandBusy))
	if !errors.Is(got, ErrCommandBusy) {
		t.Fatalf("restored error = %v, want ErrCommandBusy", got)
	}
}
