// Package rbac evaluates role grants with the vocabulary of the monolith's `accesscontrol` library
// (docs/07-auth-security.md §4): actions are createAny/readAny/updateAny/deleteAny and createOwn/readOwn/…, and an
// "any" grant implies the "own" one. Grants come from auth-service (GetGrants) and are cached by the gateway.
package rbac

import "strings"

// Grant lets Role perform Actions on Resource.
type Grant struct {
	Role       string
	Resource   string
	Actions    []string // e.g. "readAny", "updateOwn"
	Attributes string   // "*" or a comma-separated allow-list (kept for the API; not enforced here)
}

// Possession says whose resources the caller may touch.
type Possession string

const (
	None Possession = ""
	Own  Possession = "own"
	Any  Possession = "any"
)

// Evaluator answers permission questions from an immutable grant set.
type Evaluator struct {
	// role -> resource -> action -> true
	m map[string]map[string]map[string]bool
}

// New builds an Evaluator.
func New(grants []Grant) *Evaluator {
	e := &Evaluator{m: map[string]map[string]map[string]bool{}}
	for _, g := range grants {
		if e.m[g.Role] == nil {
			e.m[g.Role] = map[string]map[string]bool{}
		}
		if e.m[g.Role][g.Resource] == nil {
			e.m[g.Role][g.Resource] = map[string]bool{}
		}
		for _, a := range g.Actions {
			e.m[g.Role][g.Resource][a] = true
		}
	}
	return e
}

// Check returns the broadest possession any of roles has for verb ("create", "read", "update", "delete") on
// resource: Any, Own or None. A route guarded by `readAny` requires Any; one guarded by `readOwn` accepts both and
// the handler must then restrict the data to the caller's own.
func (e *Evaluator) Check(roles []string, verb, resource string) Possession {
	best := None
	for _, r := range roles {
		actions := e.m[r][resource]
		switch {
		case actions[verb+"Any"]:
			return Any
		case actions[verb+"Own"]:
			best = Own
		}
	}
	return best
}

// Can reports whether roles may perform the exact action, e.g. Can(roles, "readAny", "profile").
// "readOwn" is satisfied by a readAny grant too.
func (e *Evaluator) Can(roles []string, action, resource string) bool {
	verb, wantAny := split(action)
	got := e.Check(roles, verb, resource)
	if wantAny {
		return got == Any
	}
	return got == Any || got == Own
}

func split(action string) (verb string, any bool) {
	switch {
	case strings.HasSuffix(action, "Any"):
		return strings.TrimSuffix(action, "Any"), true
	case strings.HasSuffix(action, "Own"):
		return strings.TrimSuffix(action, "Own"), false
	}
	return action, true // a bare verb means the strictest reading
}
