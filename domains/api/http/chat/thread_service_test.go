package chat

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/memohai/memoh/domains/agent/adapter/acp/profile"
	messageevent "github.com/memohai/memoh/domains/agent/chat/event"
	thread "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/api/bot"
)

var errThreadStoreTestOperationUnsupported = errors.New("thread store test operation unsupported")

type threadTestStore interface {
	thread.Store
	thread.ACPPolicyReader
}

func newThreadServiceForTest(store threadTestStore) *thread.Service {
	return newThreadServiceForTestWithPublisher(store, nil)
}

func newThreadServiceForTestWithPublisher(store threadTestStore, publisher messageevent.Publisher) *thread.Service {
	service := thread.NewService(nil, store, store, publisher)
	service.SetACPSetupValidator(profile.NewCatalog())
	return service
}

// stubThreadStore is a nil-safe thread.Store base for HTTP test fakes.
type stubThreadStore struct{}

func (stubThreadStore) InTransaction(_ context.Context, _ func(thread.Store) error) error {
	return errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) CreateThread(context.Context, thread.CreateRecord) (thread.Thread, error) {
	return thread.Thread{}, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) GetThread(context.Context, string) (thread.Thread, error) {
	return thread.Thread{}, thread.ErrNotFound
}

func (stubThreadStore) ListThreadsByBot(context.Context, thread.ListRecord) ([]thread.Thread, error) {
	return nil, nil
}

func (stubThreadStore) ListThreadsByUser(context.Context, thread.ListRecord) ([]thread.Thread, error) {
	return nil, nil
}

func (stubThreadStore) ListThreadsByRoute(context.Context, string) ([]thread.Thread, error) {
	return nil, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) ListSubagentThreads(context.Context, string) ([]thread.Thread, error) {
	return nil, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) ForkThread(context.Context, thread.ForkRecord) (thread.Thread, error) {
	return thread.Thread{}, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) UpdateThreadDescriptor(context.Context, thread.DescriptorRecord) (thread.Thread, error) {
	return thread.Thread{}, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) UpdateThreadTitle(context.Context, string, string, *thread.RuntimeFence) (thread.Thread, error) {
	return thread.Thread{}, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) UpdateThreadMetadata(context.Context, string, map[string]any, *thread.RuntimeFence) (thread.Thread, error) {
	return thread.Thread{}, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) SoftDeleteThread(context.Context, string) error {
	return errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) TouchThread(context.Context, string) error {
	return errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) CountThreadMessages(context.Context, string) (int64, error) {
	return 0, nil
}

func (stubThreadStore) CreateSubagentConfig(context.Context, thread.SubagentConfigRecord) (thread.SubagentConfig, error) {
	return thread.SubagentConfig{}, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) CreateSubagentForkContext(context.Context, thread.ForkContextRecord) (int64, error) {
	return 0, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) GetSubagentConfig(context.Context, string) (thread.SubagentConfig, error) {
	return thread.SubagentConfig{}, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) ListSubagentForkContext(context.Context, string) ([]thread.SubagentForkContextMessage, error) {
	return nil, errThreadStoreTestOperationUnsupported
}

func (stubThreadStore) GetBotMetadata(context.Context, string) (map[string]any, error) {
	return nil, nil
}

func botMetadataMap(record bot.Record) map[string]any {
	return testJSONMap(record.Metadata)
}

func testJSONMap(data []byte) map[string]any {
	if len(data) == 0 {
		return nil
	}
	var value map[string]any
	_ = json.Unmarshal(data, &value)
	return value
}

func testThreadFromLegacy(id, botID, typ string, metadata []byte) thread.Thread {
	return testThreadFromLegacyFull(id, botID, typ, "", "", metadata, nil, "", "")
}

func testThreadFromLegacyFull(
	id, botID, typ, sessionMode, runtimeType string,
	metadata, runtimeMetadata []byte,
	createdByUserID, title string,
) thread.Thread {
	if !thread.IsKnownSessionMode(sessionMode) {
		sessionMode, _ = thread.DescriptorFromLegacyType(typ)
	}
	if !thread.IsKnownRuntimeType(runtimeType) {
		_, runtimeType = thread.DescriptorFromLegacyType(typ)
	}
	visibility := thread.VisibilityInternal
	if sessionMode == thread.TypeChat || sessionMode == thread.TypeDiscuss {
		visibility = thread.VisibilityUser
	}
	return thread.Thread{
		ID:              id,
		BotID:           botID,
		Type:            typ,
		SessionMode:     sessionMode,
		RuntimeType:     runtimeType,
		RuntimeMetadata: testJSONMap(runtimeMetadata),
		Title:           title,
		Metadata:        testJSONMap(metadata),
		CreatedByUserID: createdByUserID,
		Visibility:      visibility,
	}
}
