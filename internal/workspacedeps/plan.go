package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

type PlanRoot struct {
	DependencyID string         `json:"dependency_id"`
	Action       catalog.Action `json:"action"`
	Version      string         `json:"version,omitempty"`
	Ensure       bool           `json:"ensure,omitempty"`
}
type PlanNode struct {
	DependencyID string         `json:"dependency_id"`
	Name         string         `json:"name"`
	SourceURL    string         `json:"source_url"`
	RegistryID   string         `json:"registry_id"`
	Revision     string         `json:"definition_revision"`
	Digest       string         `json:"manifest_digest"`
	Requires     []string       `json:"requires"`
	RequiredBy   []string       `json:"required_by"`
	Root         bool           `json:"root"`
	Action       string         `json:"action"`
	Version      string         `json:"version,omitempty"`
	Reason       string         `json:"reason"`
	Script       *ScriptPreview `json:"script,omitempty"`
}
type Plan struct {
	ID    string     `json:"id"`
	BotID string     `json:"bot_id"`
	Roots []PlanRoot `json:"roots"`
	Nodes []PlanNode `json:"nodes"`
}
type PlanNodeResult struct {
	DependencyID string          `json:"dependency_id"`
	Action       string          `json:"action"`
	Status       string          `json:"status"`
	Operation    OperationResult `json:"-"`
	Failure      *PlanFailure    `json:"failure,omitempty"`
}
type PlanResult struct {
	Nodes []PlanNodeResult `json:"nodes"`
}
type PlanFailure struct {
	Root         string   `json:"root"`
	DependencyID string   `json:"dependency_id"`
	Path         []string `json:"path"`
	Phase        string   `json:"phase"`
	OperationID  string   `json:"operation_id,omitempty"`
	cause        error
}

func (e *PlanFailure) Error() string {
	return fmt.Sprintf("dependency %s failed during %s (root %s): %v", e.DependencyID, e.Phase, e.Root, e.cause)
}
func (e *PlanFailure) Unwrap() error { return e.cause }

type (
	planContextKey        struct{}
	nodeCatalogContextKey struct{}
)

func WithPlanID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, planContextKey{}, id)
}
func PlanID(ctx context.Context) string { id, _ := ctx.Value(planContextKey{}).(string); return id }

type PlanEvent struct {
	Version      string       `json:"version,omitempty"`
	Stream       string       `json:"stream,omitempty"`
	Data         string       `json:"data,omitempty"`
	DependencyID string       `json:"dependency_id"`
	Action       string       `json:"action"`
	Status       string       `json:"status"`
	RequiredBy   []string     `json:"required_by,omitempty"`
	Failure      *PlanFailure `json:"failure,omitempty"`
}
type planEventsKey struct{}

func WithPlanEvents(ctx context.Context, send func(PlanEvent)) context.Context {
	return context.WithValue(ctx, planEventsKey{}, send)
}

func SendPlanEvent(ctx context.Context, event PlanEvent) {
	if send, ok := ctx.Value(planEventsKey{}).(func(PlanEvent)); ok {
		send(event)
	}
}

// planOrder relies on Catalog.Validate for malformed references and cycles.
// It only determines a stable, deduplicated prerequisites-first traversal.
func planOrder(cat *catalog.Catalog, roots []PlanRoot) ([]string, error) {
	if err := cat.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
	}
	seen := map[string]bool{}
	var order []string
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		dep, ok := cat.Get(id)
		if !ok {
			return ErrDependencyNotFound
		}
		for _, child := range dep.Requires {
			if err := visit(child); err != nil {
				return err
			}
		}
		order = append(order, id)
		return nil
	}
	for _, root := range roots {
		if err := visit(root.DependencyID); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// PreparePlan freezes the whole closure before any install script executes.
// Only the server writes a plan; HTTP confirmation passes its opaque identity.
func (s *Service) PreparePlan(ctx context.Context, botID string, roots []PlanRoot) (Plan, error) {
	store, err := s.graphStore()
	if err != nil {
		return Plan{}, err
	}
	if len(roots) == 0 {
		return Plan{BotID: botID, Roots: []PlanRoot{}, Nodes: []PlanNode{}}, nil
	}
	roots = slices.Clone(roots)
	for i := range roots {
		roots[i].DependencyID = strings.TrimSpace(roots[i].DependencyID)
		roots[i].Version = strings.TrimSpace(roots[i].Version)
		if roots[i].Ensure && roots[i].Action != catalog.ActionInstall {
			return Plan{}, ErrActionUnsupported
		}
		if !ValidRequestedVersion(roots[i].Version) {
			return Plan{}, ErrInvalidVersion
		}
		switch roots[i].Action {
		case catalog.ActionInstall, catalog.ActionUpdate, catalog.ActionReinstall, ActionRollback, catalog.ActionRemove:
		default:
			return Plan{}, ErrActionUnsupported
		}
	}
	removing := roots[0].Action == catalog.ActionRemove
	for _, root := range roots {
		if root.Action == catalog.ActionRemove && len(roots) != 1 {
			return Plan{}, ErrActionUnsupported
		}
	}
	cat, err := s.operationCatalog(ctx, roots[0].DependencyID)
	if err != nil {
		return Plan{}, err
	}
	for _, requested := range roots {
		if requested.Action != ActionRollback && requested.Action != catalog.ActionRemove {
			continue
		}
		if err := s.ensureWorkspace(ctx, botID); err != nil {
			return Plan{}, err
		}
		client, root, err := s.target(ctx, botID)
		if err != nil {
			return Plan{}, err
		}
		state, err := (&operation{client: client, home: Home(root, requested.DependencyID)}).readState(ctx)
		if err != nil {
			return Plan{}, err
		}
		if requested.Action == catalog.ActionRemove {
			if state != nil && s.provider != nil {
				def, err := s.provider.StoredDefinition(ctx, DefinitionKey{state.SourceURL, requested.DependencyID, state.DefinitionRevision})
				if err != nil {
					return Plan{}, err
				}
				if def.Dependency().ManifestDigest != state.ManifestDigest {
					return Plan{}, ErrDefinitionInvalid
				}
				cat, err = cat.Using(def)
				if err != nil {
					return Plan{}, err
				}
			}
			continue
		}
		if state == nil || state.Previous == nil || state.PreviousVersion == "" {
			return Plan{}, ErrRollbackUnavailable
		}
		previous := state.Previous
		if s.provider != nil {
			def, err := s.provider.StoredDefinition(ctx, DefinitionKey{previous.SourceURL, requested.DependencyID, previous.DefinitionRevision})
			if err != nil {
				return Plan{}, err
			}
			if def.Dependency().ManifestDigest != previous.ManifestDigest {
				return Plan{}, ErrDefinitionInvalid
			}
			cat, err = cat.Using(def)
			if err != nil {
				return Plan{}, err
			}
		} else {
			dep, ok := cat.Get(requested.DependencyID)
			if !ok || dep.ManifestDigest != previous.ManifestDigest {
				return Plan{}, ErrRollbackUnavailable
			}
		}
	}
	order, err := planOrder(cat, roots)
	if err != nil {
		return Plan{}, err
	}
	if err := s.ensureWorkspace(ctx, botID); err != nil {
		return Plan{}, err
	}
	client, root, err := s.target(ctx, botID)
	if err != nil {
		return Plan{}, err
	}
	platform, err := s.platformFor(ctx, botID, client)
	if err != nil {
		return Plan{}, err
	}
	observed, err := s.discover(ctx, client, cat, root, order, platform)
	if err != nil {
		return Plan{}, err
	}
	// Reused managed tools keep the requirements of the publication that is
	// actually installed. Today's catalog is not historical installation truth.
	for attempts := 0; attempts < catalog.MaxDependencies; attempts++ {
		changed := false
		for _, id := range order {
			reuse := observed[id].Present
			for _, r := range roots {
				if r.DependencyID == id && !r.Ensure {
					reuse = false
				}
			}
			if !reuse || observed[id].Source != SourceManaged {
				continue
			}
			current, _ := cat.Get(id)
			state := observed[id].State
			if state == nil {
				return Plan{}, ErrGraphUnresolved
			}
			if current.ManifestDigest == state.ManifestDigest && current.Revision == state.DefinitionRevision {
				continue
			}
			if s.provider == nil {
				return Plan{}, ErrGraphUnresolved
			}
			def, err := s.provider.StoredDefinition(ctx, DefinitionKey{state.SourceURL, id, state.DefinitionRevision})
			if err != nil {
				return Plan{}, err
			}
			if def.Dependency().ManifestDigest != state.ManifestDigest {
				return Plan{}, ErrDefinitionInvalid
			}
			cat, err = cat.Using(def.WithRetired(current.Retired))
			if err != nil {
				return Plan{}, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
			}
			changed = true
		}
		if !changed {
			break
		}
		order, err = planOrder(cat, roots)
		if err != nil {
			return Plan{}, err
		}
		observed, err = s.discover(ctx, client, cat, root, order, platform)
		if err != nil {
			return Plan{}, err
		}
	}
	if removing {
		users, err := s.RemovalBlockers(ctx, botID, roots[0].DependencyID, "", false)
		if err != nil {
			return Plan{}, err
		}
		users = slices.DeleteFunc(users, func(user string) bool { return strings.HasPrefix(user, "unresolved:") })
		if len(users) > 0 {
			return Plan{}, &ReferencedError{roots[0].DependencyID, users}
		}
	}
	rootByID := map[string]PlanRoot{}
	parents := map[string][]string{}
	for _, r := range roots {
		if _, ok := rootByID[r.DependencyID]; ok {
			return Plan{}, ErrPlanChanged
		}
		rootByID[r.DependencyID] = r
	}
	for _, id := range order {
		dep, _ := cat.Get(id)
		for _, child := range dep.Requires {
			parents[child] = append(parents[child], id)
		}
	}
	plan := Plan{ID: uuid.NewString(), BotID: botID, Roots: roots, Nodes: []PlanNode{}}
	frozen := context.WithValue(ctx, nodeCatalogContextKey{}, cat)
	for _, id := range order {
		dep, _ := cat.Get(id)
		requested, isRoot := rootByID[id]
		node := PlanNode{DependencyID: id, Name: dep.Name, SourceURL: dep.SourceURL, RegistryID: dep.RegistryID, Revision: dep.Revision, Digest: dep.ManifestDigest, Requires: append([]string{}, dep.Requires...), RequiredBy: append([]string{}, parents[id]...), Root: isRoot, Action: "install", Reason: "required"}
		if isRoot {
			node.Action = string(requested.Action)
			node.Version = requested.Version
			node.Reason = "requested"
		}
		if (!isRoot || requested.Ensure) && observed[id].Present {
			node.Action = "reuse"
			node.Reason = "available"
			node.Version = observed[id].Version
		}
		if removing && !isRoot {
			node.Action, node.Reason = "keep", "retained"
			if hasManagedCopy(observed[roots[0].DependencyID]) && !observed[id].Present {
				return Plan{}, ErrPrerequisiteMissing
			}
		}
		if observed[id].LockHeld {
			return Plan{}, ErrBusy
		}
		if dep.Retired && !removing {
			return Plan{}, ErrDefinitionUnavailable
		}
		if node.Action != "reuse" && node.Action != "keep" {
			if !removing && !dep.SupportsPlatform(platform.OS, platform.Arch, platform.Libc) {
				return Plan{}, ErrPlatformUnsupported
			}
			if !ActionSupported(dep, catalog.Action(node.Action)) {
				return Plan{}, ErrActionUnsupported
			}
			preview, err := s.ScriptPreviewDetails(frozen, botID, id, catalog.Action(node.Action))
			if err != nil {
				return Plan{}, err
			}
			if node.Action == string(catalog.ActionInstall) || node.Action == string(catalog.ActionUpdate) || node.Action == string(catalog.ActionReinstall) {
				for i := range preview.Env {
					if preview.Env[i].Key == "MEMOH_DEP_VERSION" {
						preview.Env[i].Value = targetVersion(dep, node.Version)
						if preview.Env[i].Value == "" {
							preview.Env[i].Value = previewRequestedVersion
						}
					}
				}
			}
			node.Script = &preview
		}
		plan.Nodes = append(plan.Nodes, node)
	}
	if err := store.SavePlan(ctx, botID, plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (s *Service) frozenPlan(ctx context.Context, botID, id string) (Plan, *catalog.Catalog, error) {
	store, err := s.graphStore()
	if err != nil {
		return Plan{}, nil, err
	}
	plan, err := store.LoadPlan(ctx, botID, id)
	if err != nil {
		return Plan{}, nil, fmt.Errorf("%w: %w", ErrPlanChanged, err)
	}
	if plan.BotID != botID || len(plan.Roots) == 0 || len(plan.Nodes) == 0 {
		return Plan{}, nil, ErrPlanChanged
	}
	var definitions []catalog.Definition
	if s.provider != nil {
		current, err := s.provider.Cached(ctx)
		if err != nil {
			return Plan{}, nil, err
		}
		for _, node := range plan.Nodes {
			def, err := s.provider.StoredDefinition(ctx, DefinitionKey{node.SourceURL, node.DependencyID, node.Revision})
			if err != nil {
				return Plan{}, nil, err
			}
			dep := def.Dependency()
			latest, ok := current.Catalog.Get(node.DependencyID)
			if ((!ok || latest.Retired) && plan.Roots[0].Action != catalog.ActionRemove) || dep.ManifestDigest != node.Digest || dep.RegistryID != node.RegistryID {
				return Plan{}, nil, ErrDefinitionUnavailable
			}
			definitions = append(definitions, def)
		}
		cat, err := catalog.New(definitions)
		if err != nil {
			return Plan{}, nil, fmt.Errorf("%w: %w", ErrDefinitionInvalid, err)
		}
		return plan, cat, nil
	}
	for _, node := range plan.Nodes {
		dep, ok := s.catalog.Get(node.DependencyID)
		if !ok || dep.ManifestDigest != node.Digest || dep.Revision != node.Revision {
			return Plan{}, nil, ErrDefinitionUnavailable
		}
	}
	return plan, s.catalog, nil
}

func (s *Service) ConfirmPlan(ctx context.Context, botID, id string, roots []PlanRoot) (context.Context, error) {
	plan, _, err := s.frozenPlan(ctx, botID, id)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(plan.Roots, roots) {
		return nil, ErrPlanChanged
	}
	return WithPlanID(ctx, id), nil
}

// ExecutePlan refreshes workspace facts under the graph claim. It may omit an
// already satisfied install, but never adds an unreviewed script to a reuse.
func (s *Service) ExecutePlan(ctx context.Context, botID, id string, sink LogSink) (PlanResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := s.cancelOnShutdown(cancel)
	defer stop()
	finish, err := s.trackOperation(ctx)
	if err != nil {
		return PlanResult{}, err
	}
	defer finish()

	plan, cat, err := s.frozenPlan(ctx, botID, id)
	if err != nil {
		return PlanResult{}, err
	}
	ctx, release, err := s.AcquireGraph(ctx, botID)
	if err != nil {
		return PlanResult{}, err
	}
	defer release()
	ctx = WithPlanID(ctx, id)
	ctx = context.WithValue(ctx, nodeCatalogContextKey{}, cat)
	ctx = context.WithValue(ctx, catalogContextKey{}, CatalogResult{Catalog: cat})
	if err := s.reconcileRelationships(ctx, botID); err != nil {
		return PlanResult{}, err
	}
	if err := s.ensureWorkspace(ctx, botID); err != nil {
		return PlanResult{}, err
	}
	client, root, err := s.target(ctx, botID)
	if err != nil {
		return PlanResult{}, err
	}
	platform, err := s.platformFor(ctx, botID, client)
	if err != nil {
		return PlanResult{}, err
	}
	ids := make([]string, 0, len(plan.Nodes))
	for _, node := range plan.Nodes {
		ids = append(ids, node.DependencyID)
	}
	obs, err := s.discover(ctx, client, cat, root, ids, platform)
	if err != nil {
		return PlanResult{}, err
	}
	for _, node := range plan.Nodes {
		if obs[node.DependencyID].LockHeld {
			return PlanResult{}, ErrBusy
		}
		if node.Action == "reuse" && !obs[node.DependencyID].Present {
			return PlanResult{}, ErrPlanChanged
		}
		if node.Action == "keep" && hasManagedCopy(obs[plan.Roots[0].DependencyID]) && !obs[node.DependencyID].Present {
			return PlanResult{}, ErrPlanChanged
		}
		reusable := node.Action == "reuse" || node.Action == "keep" || !node.Root || slices.ContainsFunc(plan.Roots, func(root PlanRoot) bool { return root.DependencyID == node.DependencyID && root.Ensure })
		observed := obs[node.DependencyID]
		if reusable && observed.Present && observed.Source == SourceManaged {
			if observed.State == nil || observed.State.ManifestDigest != node.Digest || observed.State.DefinitionRevision != node.Revision {
				return PlanResult{}, ErrPlanChanged
			}
		}
		dep, _ := cat.Get(node.DependencyID)
		if node.Action != "reuse" && node.Action != "keep" && node.Action != string(catalog.ActionRemove) && !dep.SupportsPlatform(platform.OS, platform.Arch, platform.Libc) {
			return PlanResult{}, ErrPlatformUnsupported
		}
	}
	admission := ctx.Value(graphContextKey{}).(*graphAdmission)
	automatic, _ := ctx.Value(automaticContextKey{}).(bool)
	for _, node := range plan.Nodes {
		if plan.Roots[0].Action == catalog.ActionRemove {
			break
		}
		rel, exists := admission.graph[node.DependencyID]
		if !exists {
			rel.Known = true
			rel.Explicit = obs[node.DependencyID].Present && obs[node.DependencyID].Source == SourceManaged
		}
		rel.Pending = uniqueIDs(append(rel.Pending, node.Requires...))
		if node.Root && !automatic {
			rel.Explicit = true
		}
		admission.graph[node.DependencyID] = rel
	}
	if err := s.saveGraph(ctx); err != nil {
		return PlanResult{}, err
	}
	result := PlanResult{Nodes: []PlanNodeResult{}}
	failures := map[string]*PlanFailure{}
	var errs []error
	for _, node := range plan.Nodes {
		step := PlanNodeResult{DependencyID: node.DependencyID, Action: node.Action, Status: "installed"}
		for _, child := range node.Requires {
			if failure := failures[child]; failure != nil {
				blockedFailure := *failure
				if node.Root {
					blockedFailure.Root = node.DependencyID
					blockedFailure.Path = dependencyPath(Plan{Nodes: plan.Nodes, Roots: []PlanRoot{{DependencyID: node.DependencyID}}}, failure.DependencyID)
				}
				step.Failure = &blockedFailure
				step.Status = "blocked"
				break
			}
		}
		if step.Failure == nil {
			SendPlanEvent(ctx, PlanEvent{DependencyID: node.DependencyID, Action: node.Action, Status: "running", RequiredBy: node.RequiredBy})
			ensure := !node.Root
			for _, r := range plan.Roots {
				if r.DependencyID == node.DependencyID {
					ensure = r.Ensure
				}
			}
			rel := admission.graph[node.DependencyID]
			state := obs[node.DependencyID].State
			completed := rel.PlanID == id && rel.Revision == node.Revision && rel.Digest == node.Digest && obs[node.DependencyID].Present && obs[node.DependencyID].Source == SourceManaged && state != nil && rel.Version == state.Version && state.DefinitionRevision == node.Revision && state.ManifestDigest == node.Digest
			if node.Action == "reuse" || node.Action == "keep" || completed || (ensure && obs[node.DependencyID].Present) {
				step.Status = "reused"
				if node.Action == "keep" {
					step.Status = "kept"
				}
				step.Operation = OperationResult{DependencyID: node.DependencyID, Version: obs[node.DependencyID].Version}
			} else {
				// Admission is checked immediately before claiming a node. The per-node
				// durable claim and receipt still own uncertain script completion.
				store, _ := s.graphStore()
				err = store.RenewGraph(ctx, botID, admission.owner)
				if err != nil {
					cancel()
				}
				if err == nil {
					step.Operation, err = s.executeNode(ctx, botID, node, LogFunc(func(stream, line string) {
						SendPlanEvent(ctx, PlanEvent{DependencyID: node.DependencyID, Action: node.Action, Status: "log", Stream: stream, Data: line})
						if sink != nil {
							sink.Log(stream, line)
						}
					}))
				}
				if err == nil && node.Action == string(catalog.ActionRemove) {
					step.Status = "removed"
				}
				if err != nil {
					path := dependencyPath(plan, node.DependencyID)
					if step.Operation.FailurePhase == "" {
						step.Operation.FailurePhase = "admission"
					}
					failure := &PlanFailure{Root: path[0], DependencyID: node.DependencyID, Path: path, Phase: step.Operation.FailurePhase, OperationID: step.Operation.OperationID, cause: err}
					if rec, e := s.store.Get(ctx, InstallationKey{BotID: botID, DependencyID: node.DependencyID}); e == nil {
						if failure.OperationID == "" {
							failure.OperationID = rec.OperationID
						}
					}
					step.Status, step.Failure = "failed", failure
					errs = append(errs, failure)
				}
			}
		}
		if step.Failure != nil {
			failures[node.DependencyID] = step.Failure
		}
		SendPlanEvent(ctx, PlanEvent{DependencyID: node.DependencyID, Action: node.Action, Status: step.Status, Version: step.Operation.Version, RequiredBy: node.RequiredBy, Failure: step.Failure})
		result.Nodes = append(result.Nodes, step)
		if errors.Is(err, ErrOperationUncertain) || ctx.Err() != nil {
			break
		}
	}
	if len(result.Nodes) != len(plan.Nodes) && ctx.Err() != nil {
		errs = append(errs, ctx.Err())
	}
	return result, errors.Join(errs...)
}

func uniqueIDs(ids []string) []string { slices.Sort(ids); return slices.Compact(ids) }
func dependencyPath(plan Plan, target string) []string {
	nodes := map[string]PlanNode{}
	for _, n := range plan.Nodes {
		nodes[n.DependencyID] = n
	}
	var walk func(string) []string
	walk = func(id string) []string {
		if id == target {
			return []string{id}
		}
		for _, child := range nodes[id].Requires {
			if p := walk(child); p != nil {
				return append([]string{id}, p...)
			}
		}
		return nil
	}
	for _, root := range plan.Roots {
		if p := walk(root.DependencyID); p != nil {
			return p
		}
	}
	return []string{target}
}

func (s *Service) executeNode(ctx context.Context, botID string, node PlanNode, sink LogSink) (OperationResult, error) {
	if node.Action == string(catalog.ActionRemove) {
		return s.removeNode(ctx, botID, node.DependencyID, sink)
	}
	if node.Action == string(ActionRollback) {
		return s.rollbackNode(ctx, botID, node.DependencyID)
	}
	op, err := s.begin(ctx, botID, node.DependencyID, node.Version, true)
	if err != nil {
		return OperationResult{}, err
	}
	defer op.release()
	status := StatusInstalling
	if node.Action == string(catalog.ActionUpdate) {
		status = StatusUpdating
	}
	result, err := s.provision(ctx, op, catalog.Action(node.Action), status, sink)
	result.DependencyID, result.OperationID = node.DependencyID, op.operationID
	result.FailurePhase = "script"
	if op.finalizing {
		result.FailurePhase = "commit"
	}
	return result, err
}

func (s *Service) executeRoot(ctx context.Context, botID, depID, version string, action catalog.Action, sink LogSink) (OperationResult, error) {
	roots := []PlanRoot{{DependencyID: strings.TrimSpace(depID), Action: action, Version: strings.TrimSpace(version)}}
	id := PlanID(ctx)
	if id == "" {
		if RequiresConfirmedPlan(ctx) {
			return OperationResult{}, ErrPlanChanged
		}
		plan, err := s.PreparePlan(ctx, botID, roots)
		if err != nil {
			return OperationResult{}, err
		}
		id = plan.ID
	} else {
		var err error
		ctx, err = s.ConfirmPlan(ctx, botID, id, roots)
		if err != nil {
			return OperationResult{}, err
		}
	}
	result, err := s.ExecutePlan(ctx, botID, id, sink)
	for _, node := range result.Nodes {
		if node.DependencyID == depID {
			return node.Operation, err
		}
	}
	return OperationResult{}, err
}

type requirePlanKey struct{}

func RequireConfirmedPlan(ctx context.Context) context.Context {
	return context.WithValue(ctx, requirePlanKey{}, true)
}

func RequiresConfirmedPlan(ctx context.Context) bool {
	required, _ := ctx.Value(requirePlanKey{}).(bool)
	return required
}

// PlanScriptPreview returns script details from the confirmed closure, without
// mixing a historical root with newly published prerequisites during HTTP admission.
func (s *Service) PlanScriptPreview(ctx context.Context, botID, id string, root PlanRoot) (ScriptPreview, error) {
	plan, cat, err := s.frozenPlan(ctx, botID, id)
	if err != nil {
		return ScriptPreview{}, err
	}
	if !slices.Equal(plan.Roots, []PlanRoot{root}) {
		return ScriptPreview{}, ErrPlanChanged
	}
	return s.ScriptPreviewDetails(context.WithValue(ctx, nodeCatalogContextKey{}, cat), botID, root.DependencyID, root.Action)
}
