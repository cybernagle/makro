package role

// Store is the in-memory cache of loaded roles. Phase 1 holds only A-level
// roles (from roles.toml). Phase 2 will extend it to hold B-level
// (runtime-created) roles in the same lookup.
type Store struct {
	roles map[string]Role
	order []string // insertion order, for Default() = "first"
}

// NewStore builds a Store from a slice (typically the loader output).
// Duplicate names in the input are deduped to the last entry; the loader
// already rejects duplicates, so this is defensive only.
func NewStore(roles []Role) *Store {
	s := &Store{
		roles: make(map[string]Role, len(roles)),
		order: make([]string, 0, len(roles)),
	}
	for _, r := range roles {
		if _, exists := s.roles[r.Name]; !exists {
			s.order = append(s.order, r.Name)
		}
		s.roles[r.Name] = r
	}
	return s
}

// Len returns the number of roles.
func (s *Store) Len() int { return len(s.roles) }

// Has reports whether a role with the given name exists.
func (s *Store) Has(name string) bool { _, ok := s.roles[name]; return ok }

// Get returns the role by name. ok is false if not found.
func (s *Store) Get(name string) (Role, bool) {
	r, ok := s.roles[name]
	return r, ok
}

// All returns a defensive copy of all roles, in insertion order.
func (s *Store) All() []Role {
	out := make([]Role, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.roles[name])
	}
	return out
}

// Default returns the fallback role for ambiguous routing (spec §3.2 step 4):
// a role literally named "default" if present, otherwise the first role in
// insertion order, otherwise ok=false for an empty store.
func (s *Store) Default() (Role, bool) {
	if r, ok := s.roles["default"]; ok {
		return r, true
	}
	if len(s.order) == 0 {
		return Role{}, false
	}
	return s.roles[s.order[0]], true
}
