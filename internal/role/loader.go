package role

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// tomlFile mirrors the on-disk shape of roles.toml. Unknown keys are
// tolerated because BurntSushi/toml's Decode does not error on extra fields
// by default — this is the forward-compatibility hook (spec §12).
type tomlFile struct {
	Role []Role `toml:"role"`
}

// LoadFile parses a roles.toml. A missing file returns an empty list with no
// error (so a fresh install with no roles.toml is fine). Malformed TOML or a
// role failing Validate produces an error.
func LoadFile(path string) ([]Role, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var tf tomlFile
	if _, err := toml.Decode(string(data), &tf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := make(map[string]bool, len(tf.Role))
	for i := range tf.Role {
		r := &tf.Role[i]
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("role[%d]: %w", i, err)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("duplicate role name %q", r.Name)
		}
		seen[r.Name] = true
		r.applyDefaults()
	}
	return tf.Role, nil
}
