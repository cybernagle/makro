package tools

func AllTools(tc TmuxClient, gm Guardian) []Tool {
	ts := []Tool{
		NewListSessionsTool(tc),
		NewCreateSessionTool(tc),
		NewSwitchSessionTool(tc),
		NewKillSessionTool(tc),
		NewSendToSessionTool(tc, gm),
		NewReadSessionOutputTool(tc),
		NewReadStructuredOutputTool(tc),
		NewRelayMessageTool(tc, gm),
		NewSaveContextTool(tc),
		NewRestoreContextTool(tc),
		NewWaitUntilIdleTool(tc),
		NewSetStateTool(),
		NewGetStateTool(),
	}
	if gm != nil {
		ts = append(ts,
			NewWatchSessionTool(gm),
			NewUnwatchSessionTool(gm),
			NewListWatchersTool(gm),
		)
	}
	return ts
}
