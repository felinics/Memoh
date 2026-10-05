package errs

import (
	"context"
	stderrors "errors"
	"log/slog"
	"runtime"
	"strings"
	"testing"
)

func TestStackOrigin(t *testing.T) {
	_, file, line, _ := runtime.Caller(0)
	err := New("origin") // line + 1
	r := Analyze(context.Background(), WrapDependency(err, "outer"))
	if r.Source == nil || r.Source.File != file || r.Source.Line != line+1 {
		t.Fatalf("first frame = %+v, want %s:%d", r.Source, file, line+1)
	}
	if len(r.Stack) == 0 || len(r.Stack) > 16 || r.Stack[0] != *r.Source {
		t.Fatalf("stack = %+v, source = %+v", r.Stack, r.Source)
	}
	if Wrap(nil, "ignored") != nil || WrapDependency(nil, "ignored") != nil {
		t.Fatal("wrapping nil must return nil")
	}
}

// wrapProviderFailure is a wrapping helper: the source must be the line that
// calls it, not this one.
func wrapProviderFailure(err error, dependency bool) error {
	if dependency {
		return WrapDependencyWithDepth(1, err, "provider call")
	}
	return WrapWithDepth(1, err, "provider call")
}

func invalidSetting(field string) error {
	return NewWithDepth(1, "invalid setting", slog.String("field", field))
}

func TestNewWithDepthLocatesHelperCaller(t *testing.T) {
	_, file, line, _ := runtime.Caller(0)
	err := invalidSetting("app_id") // line + 1
	r := Analyze(context.Background(), err)
	if r.Source == nil || r.Source.File != file || r.Source.Line != line+1 {
		t.Fatalf("source = %+v, want %s:%d", r.Source, file, line+1)
	}
	if r.Fault != FaultServer {
		t.Fatalf("fault = %s, want server", r.Fault)
	}
}

func TestWithDepthLocatesHelperCaller(t *testing.T) {
	for _, dependency := range []bool{true, false} {
		_, file, line, _ := runtime.Caller(0)
		err := wrapProviderFailure(stderrors.New("down"), dependency) // line + 1
		r := Analyze(context.Background(), err)
		if r.Source == nil || r.Source.File != file || r.Source.Line != line+1 {
			t.Fatalf("dependency=%t source = %+v, want %s:%d", dependency, r.Source, file, line+1)
		}
		if want := map[bool]Fault{true: FaultDependency, false: FaultServer}[dependency]; r.Fault != want {
			t.Fatalf("dependency=%t fault = %s, want %s", dependency, r.Fault, want)
		}
	}
	if WrapWithDepth(1, nil, "ignored") != nil || WrapDependencyWithDepth(1, nil, "ignored") != nil {
		t.Fatal("wrapping nil must return nil")
	}
}

func panicAt(v any) (err error) {
	defer func() {
		err = Wrap(Recovered(recover()), "job")
	}()
	panic(v) // panic site
}

func TestRecovered(t *testing.T) {
	err := panicAt("secret user input")
	r := Analyze(context.Background(), err)
	if !r.Panic || r.Fault != FaultServer || r.Unlocated {
		t.Fatalf("report=%+v", r)
	}
	if strings.Contains(r.Text, "secret") || r.Text != "job: panic" {
		t.Fatalf("text=%q", r.Text)
	}
	if r.Source == nil || !strings.HasSuffix(r.Source.Function, ".panicAt") {
		t.Fatalf("source=%+v, want panicAt", r.Source)
	}

	var m map[string]int
	err = func() (err error) {
		defer func() { err = Recovered(recover()) }()
		m["x"] = 1
		return nil
	}()
	if got := Text(err); got != "panic: assignment to entry in nil map" {
		t.Fatalf("text=%q", got)
	}
	if Analyze(context.Background(), New("plain")).Panic {
		t.Fatal("plain error must not be panic")
	}
}
