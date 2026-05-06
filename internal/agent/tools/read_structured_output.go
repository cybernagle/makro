package tools

import (
	"context"
	"fmt"
)

func NewReadStructuredOutputTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "read_structured_output",
		Description: "Read and parse a session's output into structured JSON with status, messages, errors, and file paths",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			out, err := ReadStructuredOutput(tc, name)
			if err != nil {
				return "", err
			}
			return structuredToJSON(out)
		},
	}
}
