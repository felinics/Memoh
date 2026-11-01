package handlers

import (
	"context"

	"github.com/google/uuid"

	acpprofileadapter "github.com/felinics/memoh/internal/agent/adapter/acpprofile"
	runtimeprofile "github.com/felinics/memoh/internal/agent/runtime/external/acp/profile"
	"github.com/felinics/memoh/internal/botagents"
	thread "github.com/felinics/memoh/internal/chat/thread"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func newThreadServiceForTest(queries dbstore.Queries) *thread.Service {
	service := thread.NewService(nil, queries, nil)
	service.SetACPSetupValidator(acpprofileadapter.NewCatalog(testACPInstances{}))
	return service
}

// testACPInstances stands in for a bot's Agent instances: every ACP profile
// has one, whose id is derived from the profile, and its setup is the bot's
// profile slot.
type testACPInstances struct{}

func testACPInstanceID(provider string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("acp-instance:"+provider)).String()
}

func (testACPInstances) FindActiveByProvider(_ context.Context, botID, provider string) (botagents.BotAgent, error) {
	return botagents.BotAgent{
		ID:       testACPInstanceID(provider),
		BotID:    botID,
		Runtime:  botagents.RuntimeACP,
		Enabled:  true,
		Metadata: map[string]any{botagents.MetadataProviderKey: provider},
	}, nil
}

func (i testACPInstances) Get(ctx context.Context, botID, id string) (botagents.BotAgent, error) {
	for _, profile := range runtimeprofile.List() {
		if testACPInstanceID(profile.ID) == id {
			return i.FindActiveByProvider(ctx, botID, profile.ID)
		}
	}
	return botagents.BotAgent{}, botagents.ErrNotFound
}

func (testACPInstances) ResolveACPSetup(_ context.Context, _, _, provider string, botMetadata map[string]any) (runtimeprofile.AgentSetup, error) {
	return runtimeprofile.ParseAgentSetup(botMetadata, provider), nil
}
