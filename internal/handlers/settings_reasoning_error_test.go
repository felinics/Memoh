package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/reasoning"
	"github.com/felinics/memoh/internal/settings"
)

func TestSettingsReasoningHTTPError(t *testing.T) {
	t.Parallel()

	invalid := &settings.InvalidReasoningEffortError{
		Effort: reasoning.EffortXHigh,
		Options: reasoning.Options{
			Supported: true,
			Efforts:   []string{reasoning.EffortLow, reasoning.EffortHigh},
		},
	}
	mapped := settingsReasoningHTTPError(invalid)
	if got := apperror.CodeOf(mapped); got != apperror.CodeSettingsReasoningEffortInvalid {
		t.Fatalf("invalid effort code = %q, want %q", got, apperror.CodeSettingsReasoningEffortInvalid)
	}
	problem, ok := apperror.ProblemFrom(mapped, "request-reasoning")
	if !ok {
		t.Fatal("invalid effort did not map to Problem Details")
	}
	if problem.Status != 400 || problem.Args["effort"] != reasoning.EffortXHigh {
		t.Fatalf("invalid effort problem = %#v", problem)
	}

	private := errors.New("SECRET provider diagnostic")
	unavailable := settingsReasoningHTTPError(errors.Join(settings.ErrReasoningOptionsUnavailable, private))
	if got := apperror.CodeOf(unavailable); got != apperror.CodeSettingsReasoningUnavailable {
		t.Fatalf("unavailable code = %q, want %q", got, apperror.CodeSettingsReasoningUnavailable)
	}
	problem, ok = apperror.ProblemFrom(unavailable, "request-reasoning")
	if !ok || problem.Status != 503 {
		t.Fatalf("unavailable problem = %#v, ok = %t", problem, ok)
	}
	if strings.Contains(problem.Detail, "SECRET") {
		t.Fatalf("Problem Details leaked private diagnostic: %#v", problem)
	}
	if !errors.Is(apperror.CauseOf(unavailable), private) {
		t.Fatalf("private cause was not retained for logging: %v", apperror.CauseOf(unavailable))
	}
}
