package workspacedeps

import (
	"context"
	"encoding/json"
)

func cloneJSON[T any](value T) T {
	data, _ := json.Marshal(value)
	var out T
	_ = json.Unmarshal(data, &out)
	return out
}

func (f *fakeStore) SavePlan(_ context.Context, bot string, p Plan) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.plans == nil {
		f.plans = map[string]Plan{}
	}
	f.plans[bot+":"+p.ID] = cloneJSON(p)
	return nil
}

func (f *fakeStore) LoadPlan(_ context.Context, bot, id string) (Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.plans[bot+":"+id]
	if !ok {
		return Plan{}, ErrPlanChanged
	}
	return cloneJSON(p), nil
}

func (f *fakeStore) ReadGraph(_ context.Context, bot string) (DependencyGraph, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneJSON(f.graphs[bot]), nil
}

func (f *fakeStore) ClaimGraph(_ context.Context, bot, owner string) (DependencyGraph, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners == nil {
		f.owners = map[string]string{}
	}
	if f.owners[bot] != "" {
		return nil, ErrBusy
	}
	for _, rec := range f.records {
		if rec.BotID == bot && rec.Status.InProgress() {
			return nil, ErrBusy
		}
	}
	f.owners[bot] = owner
	return cloneJSON(f.graphs[bot]), nil
}

func (f *fakeStore) RenewGraph(_ context.Context, bot, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners[bot] != owner {
		return ErrBusy
	}
	return nil
}

func (f *fakeStore) WriteGraph(_ context.Context, bot, owner string, g DependencyGraph) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners[bot] != owner {
		return ErrBusy
	}
	if f.graphs == nil {
		f.graphs = map[string]DependencyGraph{}
	}
	f.graphs[bot] = cloneJSON(g)
	return nil
}

func (f *fakeStore) ReleaseGraph(_ context.Context, bot, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners[bot] == owner {
		delete(f.owners, bot)
	}
	return nil
}

func (*fakeStore) AppUsers(context.Context, string, string) ([]AppDependencyUser, error) {
	return nil, nil
}
