package slash

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
)

// A refusal is answered with its catalog code at every boundary, and callers
// in the process still match it as a slash refusal.
func TestErrorIsAnsweredWithItsCatalogCode(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("resolve skills: %w", NewError(CodeRequestedSkillAmbiguous))
	public, fault := errs.Answer(context.Background(), err)
	if apperror.CodeOf(public) != apperror.CodeSlashSkillAmbiguous || fault != apperror.FaultClient {
		t.Fatalf("answer = %v %s, want %s from the client", public, fault, apperror.CodeSlashSkillAmbiguous)
	}
	var refusal Error
	if !errors.As(err, &refusal) || refusal.Code != CodeRequestedSkillAmbiguous {
		t.Fatalf("errors.As = %#v, want the refusal", refusal)
	}
}
