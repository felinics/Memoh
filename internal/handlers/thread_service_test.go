package handlers

import (
	acpprofileadapter "github.com/felinics/memoh/internal/agent/adapter/acpprofile"
	thread "github.com/felinics/memoh/internal/chat/thread"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func newThreadServiceForTest(queries dbstore.Queries) *thread.Service {
	service := thread.NewService(nil, queries, nil)
	service.SetACPSetupValidator(acpprofileadapter.NewCatalog())
	return service
}
