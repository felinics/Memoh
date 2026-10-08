package serverruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/errs"
	runtimeRpc "github.com/felinics/memoh/internal/rpc/runtime"
)

// The deployment mode does not change the answer a user gets: a command's
// catalog error is attributed the same whether the channel called the
// command handler in its own process or over the RPC.
func TestCommandErrorIsAnsweredAlikeInProcessAndOverRPC(t *testing.T) {
	for name, sent := range map[string]error{
		"client code":              apperror.New(apperror.CodeBotNameTaken, nil),
		"declared dependency code": apperror.Wrap(apperror.CodeAgentProviderRateLimited, errors.New("api error 429"), nil),
	} {
		t.Run(name, func(t *testing.T) {
			client := newQueueClient(t, map[string]runtimeRpc.Handler{
				MethodCommandExecute: func(context.Context, json.RawMessage) (any, error) { return nil, sent },
			})
			_, err := client.ExecuteResult(context.Background(), command.ExecuteInput{})
			localAnswer, localFault := errs.Answer(context.Background(), sent)
			remoteAnswer, remoteFault := errs.Answer(context.Background(), err)
			if remoteFault != localFault || apperror.CodeOf(remoteAnswer) != apperror.CodeOf(localAnswer) || errs.Analyze(context.Background(), err).Reason != errs.Analyze(context.Background(), sent).Reason {
				t.Fatalf("over RPC answer=%s %s; in process answer=%s %s", apperror.CodeOf(remoteAnswer), remoteFault, apperror.CodeOf(localAnswer), localFault)
			}
		})
	}
}
