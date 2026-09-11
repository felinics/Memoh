package apps

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type removalConnectors struct {
	*fakeConnectors
	err error
}

func (c *removalConnectors) Delete(ctx context.Context, bot, connection string) error {
	if c.err != nil {
		return c.err
	}
	return c.fakeConnectors.Delete(ctx, bot, connection)
}

type removalStore struct {
	Store
	deleteErr error
}

func (s *removalStore) Delete(ctx context.Context, bot, id string) (Installation, error) {
	if s.deleteErr != nil {
		return Installation{}, s.deleteErr
	}
	return s.Store.Delete(ctx, bot, id)
}

type removalPublisher struct {
	*fakePublisher
	commitErr error
}

type removalTx struct {
	SkillTransaction
	publisher *removalPublisher
}

func (tx removalTx) Commit(ctx context.Context) error {
	if tx.publisher.commitErr != nil {
		return tx.publisher.commitErr
	}
	return tx.SkillTransaction.Commit(ctx)
}

func (p *removalPublisher) RemoveSkills(ctx context.Context, bot, target, registry, app, revision string) (SkillTransaction, error) {
	tx, err := p.fakePublisher.RemoveSkills(ctx, bot, target, registry, app, revision)
	return removalTx{SkillTransaction: tx, publisher: p}, err
}

func TestRemoveRetainsFailedCleanupUntilRetry(t *testing.T) {
	for _, fault := range []string{"dependency", "connector", "drop_dependency_ref", "drop_connector_ref", "skills_commit", "installation_delete"} {
		t.Run(fault, func(t *testing.T) {
			f := newCleanupFixture(t)
			conns := &removalConnectors{fakeConnectors: f.connectors}
			store := &removalStore{Store: f.storeFaults}
			publisher := &removalPublisher{fakePublisher: f.publisher}
			f.service.connectors, f.service.store, f.service.skills = conns, store, publisher
			switch fault {
			case "dependency":
				f.depFaults.removeErr = errCleanupProbe
			case "connector":
				conns.err = errCleanupProbe
			case "skills_commit":
				publisher.commitErr = errCleanupProbe
			case "installation_delete":
				store.deleteErr = errCleanupProbe
			default:
				f.storeFaults.failure = fault
			}
			rec := &recorder{}
			_, err := f.service.Remove(t.Context(), testBotID, f.inst.ID, RemoveOptions{}, rec)
			assertCleanupFailed(t, f, rec, err)
			if !strings.HasPrefix(f.installation(t).LastError, "App removal failed:") {
				t.Fatal("removal failure must identify the failed operation")
			}
			if !errors.Is(err, errCleanupProbe) {
				t.Fatalf("lost error cause: %v", err)
			}
			for _, event := range rec.events {
				if strings.Contains(event.Message, errCleanupProbe.Error()) {
					t.Fatalf("raw cause leaked: %+v", event)
				}
			}
			if fault == "connector" {
				refs, _ := f.store.ListConnectorRefs(t.Context(), f.inst.ID)
				if len(refs) != 1 || refs[0].ConnectionID != "github-1" || len(f.connectors.connections) != 1 {
					t.Fatal("failed revocation lost its retry target")
				}
			}
			f.depFaults.removeErr, conns.err, publisher.commitErr, store.deleteErr = nil, nil, nil, nil
			f.storeFaults.failure = ""
			f.restartService()
			rec = &recorder{}
			if _, err := f.service.Remove(t.Context(), testBotID, f.inst.ID, RemoveOptions{}, rec); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.GetByID(t.Context(), testBotID, f.inst.ID); !errors.Is(err, ErrNotInstalled) {
				t.Fatalf("installation still present: %v", err)
			}
			if len(f.deps.removed) != 1 || len(f.connectors.connections) != 0 || !strings.HasSuffix(rec.types(), "done=removed") {
				t.Fatalf("incomplete or repeated cleanup: deps=%v connections=%v events=%s", f.deps.removed, f.connectors.connections, rec.types())
			}
		})
	}
}

func TestDiscoveryErrorNeverBecomesPublicFailureText(t *testing.T) {
	f := newCleanupFixture(t)
	view := f.deps.list()
	view.DiscoveryError = "bridge dial 10.0.0.8 password=synthetic-test-secret"
	f.depFaults.view = &view
	_, err := f.service.Update(t.Context(), testBotID, f.inst.ID, &recorder{})
	if err == nil || !strings.Contains(err.Error(), view.DiscoveryError) {
		t.Fatalf("logs must retain the cause: %v", err)
	}
	got := f.installation(t).LastError
	if strings.Contains(got, "synthetic-test-secret") || strings.Contains(got, "10.0.0.8") || !strings.Contains(got, genericPublicCause) {
		t.Fatalf("unsafe persisted error: %q", got)
	}
}

func TestRemoveRequiredAppFailureKeepsParentRetryable(t *testing.T) {
	f := newCleanupFixture(t)
	child := release("memoh", "node-runtime", "c", "1", nil, []string{"node"}, nil)
	f.publish(child)
	installed, err := f.service.Install(t.Context(), testBotID, InstallRequest{RegistryID: "memoh", AppID: child.AppID, Revision: child.Revision, Reason: ReasonRequired}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.service.store = &removalStore{Store: f.store, deleteErr: errCleanupProbe}
	rec := &recorder{}
	_, err = f.service.Remove(t.Context(), testBotID, f.inst.ID, RemoveOptions{RemoveUnreferencedRequired: true}, rec)
	assertCleanupFailed(t, f, rec, err)
	f.restartService()
	rec = &recorder{}
	if _, err := f.service.Remove(t.Context(), testBotID, f.inst.ID, RemoveOptions{RemoveUnreferencedRequired: true}, rec); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.inst.ID, installed.Installation.ID} {
		if _, err := f.store.GetByID(t.Context(), testBotID, id); !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("removal did not finish %s: %v", id, err)
		}
	}
	if strings.Count(rec.types(), "done=removed") != 1 {
		t.Fatalf("nested completion escaped before parent finished: %s", rec.types())
	}
}
