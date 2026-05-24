package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/naglezhang/makro/internal/util"
)

func NewAssessConfirmationTool(tc TmuxClient, assessor Assessor) Tool {
	return Tool{
		Name:        "assess_confirmation",
		Description: "Read a session's output and assess whether a pending confirmation prompt should be approved or rejected. Returns the decision and reason.",
		Parameters: []Param{
			{Name: "session_name", Type: "string", Description: "Session name to assess", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			sessionName, _ := args["session_name"].(string)
			if sessionName == "" {
				return "", fmt.Errorf("session_name is required")
			}

			if assessor == nil {
				return "", fmt.Errorf("no assessor configured")
			}

			out, err := ReadStructuredOutput(tc, sessionName)
			if err != nil {
				return "", err
			}

			output := util.ReadProgressive(out.RawOutput, 2000)
			assessment, err := assessor.Assess(ctx, sessionName, output)
			if err != nil {
				return "", fmt.Errorf("assess: %w", err)
			}

			data, _ := json.Marshal(map[string]string{
				"decision": assessment.Decision,
				"reason":   assessment.Reason,
			})
			return string(data), nil
		},
	}
}

// AssessWithTimeout wraps assess_confirmation with a default timeout.
func AssessWithTimeout(ctx context.Context, assessor Assessor, sessionName, output string, timeout time.Duration) (*Assessment, error) {
	assessCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return assessor.Assess(assessCtx, sessionName, output)
}
