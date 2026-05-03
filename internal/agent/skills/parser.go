package skills

import (
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

var errNoName = errors.New("skill must have a name in frontmatter")

type frontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	AllowedTools []string `yaml:"allowed-tools"`
	Arguments    []string `yaml:"arguments"`
}

// ParseSkill parses a SKILL.md file content into a Skill.
func ParseSkill(content string) (*Skill, error) {
	fm, body, ok := splitFrontmatter(content)
	if !ok {
		body = content
		fm = &frontmatter{}
	}

	if fm.Name == "" {
		return nil, errNoName
	}

	return &Skill{
		Name:         fm.Name,
		Description:  fm.Description,
		Prompt:       strings.TrimSpace(body),
		AllowedTools: fm.AllowedTools,
		Args:         fm.Arguments,
	}, nil
}

func splitFrontmatter(content string) (*frontmatter, string, bool) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return nil, content, false
	}

	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return nil, content, false
	}

	fmText := strings.TrimSpace(rest[:end])
	body := rest[end+4:]

	fm := &frontmatter{}
	if err := yaml.Unmarshal([]byte(fmText), fm); err != nil {
		return nil, "", false
	}
	return fm, body, true
}
