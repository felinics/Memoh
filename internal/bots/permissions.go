package bots

import (
	"encoding/json"
	"strings"
)

// vocabulary is a closed set of permission scopes plus the implications between
// them. Bot access has two of these: one for people who use a bot, one for peer
// bots that reach it. They are deliberately disjoint — a scope that leaks from
// one vocabulary into the other is a privilege bug, not a naming inconsistency,
// so every encode/decode path goes through the vocabulary that owns it.
type vocabulary struct {
	// ordered is the canonical serialization order, weakest scope first. Stored
	// permission arrays and API responses both use it, so a grant's JSON is
	// stable regardless of the order the caller sent.
	ordered []string
	// closure maps each scope to itself plus everything it implies, transitively.
	closure map[string][]string
}

// newVocabulary precomputes the transitive closure of implies so expansion is a
// single pass at runtime. Implication chains are short and acyclic by
// construction; a cycle would simply saturate to the same fixpoint.
func newVocabulary(ordered []string, implies map[string][]string) *vocabulary {
	v := &vocabulary{ordered: ordered, closure: make(map[string][]string, len(ordered))}
	for _, scope := range ordered {
		seen := map[string]bool{scope: true}
		queue := []string{scope}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			for _, implied := range implies[current] {
				if !seen[implied] {
					seen[implied] = true
					queue = append(queue, implied)
				}
			}
		}
		v.closure[scope] = v.canonical(seen)
	}
	return v
}

// canonical projects a scope set onto the vocabulary's declared order.
func (v *vocabulary) canonical(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for _, scope := range v.ordered {
		if seen[scope] {
			out = append(out, scope)
		}
	}
	return out
}

// all returns every scope in the vocabulary.
func (v *vocabulary) all() []string {
	out := make([]string, len(v.ordered))
	copy(out, v.ordered)
	return out
}

func (v *vocabulary) known(scope string) bool {
	_, ok := v.closure[scope]
	return ok
}

// normalize validates caller input: unknown scopes are rejected rather than
// dropped, so a typo in an API request cannot silently narrow a grant.
func (v *vocabulary) normalize(raw []string) ([]string, error) {
	seen := map[string]bool{}
	for _, scope := range raw {
		key := strings.ToLower(strings.TrimSpace(scope))
		if key == "" {
			continue
		}
		if !v.known(key) {
			return nil, ErrInvalidPermission
		}
		seen[key] = true
	}
	out := v.expandSet(seen)
	if len(out) == 0 {
		return nil, ErrInvalidPermission
	}
	return out, nil
}

// expand is the lenient counterpart of normalize, for data already stored:
// scopes this build no longer knows are dropped instead of failing the read.
func (v *vocabulary) expand(raw []string) []string {
	seen := make(map[string]bool, len(raw))
	for _, scope := range raw {
		key := strings.ToLower(strings.TrimSpace(scope))
		if v.known(key) {
			seen[key] = true
		}
	}
	return v.expandSet(seen)
}

func (v *vocabulary) expandSet(seen map[string]bool) []string {
	expanded := make(map[string]bool, len(seen))
	for scope := range seen {
		for _, implied := range v.closure[scope] {
			expanded[implied] = true
		}
	}
	return v.canonical(expanded)
}

// has reports whether the granted set satisfies required, after expansion.
func (v *vocabulary) has(granted []string, required string) bool {
	required = strings.ToLower(strings.TrimSpace(required))
	if !v.known(required) {
		return false
	}
	for _, scope := range v.expand(granted) {
		if scope == required {
			return true
		}
	}
	return false
}

func (v *vocabulary) decode(payload []byte) []string {
	if len(payload) == 0 {
		return nil
	}
	var scopes []string
	if err := json.Unmarshal(payload, &scopes); err != nil {
		return nil
	}
	return v.expand(scopes)
}

func (*vocabulary) encode(scopes []string) ([]byte, error) {
	if scopes == nil {
		scopes = []string{}
	}
	return json.Marshal(scopes)
}
