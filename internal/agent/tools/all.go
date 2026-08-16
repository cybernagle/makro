package tools

// AllTools builds the orchestrator's toolset. healer wires programmatic
// session recovery into send_to_session (and any gated send): a session whose
// agent died is relaunched from the snapshot instead of erroring out.
func AllTools(tc TmuxClient, assessor Assessor, cwd string, notifier Notifier, healer SessionHealer) []Tool {
	return []Tool{
		NewListSessionsTool(tc),
		NewCreateSessionTool(tc),
		NewSwitchSessionTool(tc),
		NewSendToSessionTool(tc, notifier, healer),
		NewReadSessionOutputTool(tc),
		NewReadStructuredOutputTool(tc),
		NewRelayMessageTool(tc, healer),
		NewSaveContextTool(tc),
		NewRestoreContextTool(tc, healer),
		NewWaitUntilIdleTool(tc, notifier),
		NewAssessConfirmationTool(tc, assessor),
		NewRespondConfirmationTool(tc),
		NewSetStateTool(),
		NewGetStateTool(),
		NewReadFileTool(cwd),
		NewWriteFileTool(cwd),
		NewListDirectoryTool(cwd),
	}
}
