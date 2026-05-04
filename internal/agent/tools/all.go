package tools

func AllTools(tc TmuxClient, assessor Assessor, cwd string) []Tool {
	return []Tool{
		NewListSessionsTool(tc),
		NewCreateSessionTool(tc),
		NewSwitchSessionTool(tc),
		NewKillSessionTool(tc),
		NewSendToSessionTool(tc),
		NewReadSessionOutputTool(tc),
		NewReadStructuredOutputTool(tc),
		NewRelayMessageTool(tc),
		NewSaveContextTool(tc),
		NewRestoreContextTool(tc),
		NewWaitUntilIdleTool(tc),
		NewAssessConfirmationTool(tc, assessor),
		NewRespondConfirmationTool(tc),
		NewSetStateTool(),
		NewGetStateTool(),
		NewReadFileTool(cwd),
		NewWriteFileTool(cwd),
		NewListDirectoryTool(cwd),
	}
}
