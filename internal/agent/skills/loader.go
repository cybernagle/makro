package skills

import (
	"os"
	"path/filepath"
)

// LoadFromDir scans a directory for skill subdirectories containing SKILL.md
// files and returns the parsed skills keyed by name.
func LoadFromDir(dir string) (map[string]*Skill, error) {
	skills := make(map[string]*Skill)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return skills, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		skill, err := ParseSkill(string(data))
		if err != nil {
			continue
		}
		skills[skill.Name] = skill
	}

	return skills, nil
}

// LoadAll loads skills from multiple directories. Later directories override
// earlier ones for skills with the same name.
func LoadAll(dirs []string) (map[string]*Skill, error) {
	all := make(map[string]*Skill)
	for _, dir := range dirs {
		fromDir, err := LoadFromDir(dir)
		if err != nil {
			return nil, err
		}
		for name, skill := range fromDir {
			all[name] = skill
		}
	}
	return all, nil
}
