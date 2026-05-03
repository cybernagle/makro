package skills

import (
	"fmt"
	"regexp"
	"strings"
)

// Skill represents a reusable prompt template loaded from a SKILL.md file.
type Skill struct {
	Name         string
	Description  string
	Prompt       string
	AllowedTools []string
	Args         []string
}

// ExpandPrompt replaces template variables in the skill prompt with the
// provided argument values. Supported templates: {{.Args}} (all args joined),
// {{.Arg0}}, {{.Arg1}}, etc.
var argPlaceholder = regexp.MustCompile(`\{\{\.Arg\d+\}\}`)

func (s *Skill) ExpandPrompt(args []string) string {
	p := s.Prompt
	p = strings.ReplaceAll(p, "{{.Args}}", strings.Join(args, " "))
	for i, a := range args {
		p = strings.ReplaceAll(p, fmt.Sprintf("{{.Arg%d}}", i), a)
	}
	p = argPlaceholder.ReplaceAllString(p, "")
	return p
}
