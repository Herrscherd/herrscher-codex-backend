package codex

const (
	fieldMethod      = "method"
	fieldResult      = "result"
	fieldText        = "text"
	fieldThreadID    = "threadId"
	fieldTurnID      = "turnId"
	fieldItemID      = "itemId"
	fieldTurn        = "turn"
	fieldThread      = "thread"
	fieldItem        = "item"
	fieldType        = "type"
	fieldID          = "id"
	fieldError       = "error"
	fieldMessage     = "message"
	fieldWillRetry   = "willRetry"
	fieldStatus      = "status"
	fieldExitCode    = "exitCode"
	fieldDelta       = "delta"
	fieldInput       = "input"
	fieldCommand     = "command"
	fieldFileChanges = "fileChanges"
	toolCommand      = "command"
	toolBash         = "Bash"
	toolWrite        = "Write"
	fieldParams      = "params"
	methodError      = "error"
	methodTurnStart  = "turn/started"
	methodTurnEnd    = "turn/completed"
	methodItemStart  = "item/started"
	methodItemEnd    = "item/completed"
	methodAgentText  = "item/agentMessage/delta"
	statusCompleted  = "completed"
	statusFailed     = "failed"
	statusDeclined   = "declined"
	contentText      = "text"
	eventResult      = "result"
	eventText        = "text"
	codexName        = "codex"
	kindStream       = "stream"
	kindOneShot      = "oneshot"
)

func stringField(fields map[string]any, key string) string {
	value, ok := fields[key].(string)
	if !ok {
		return ""
	}
	return value
}

func numberField(fields map[string]any, key string) (float64, bool) {
	value, ok := fields[key].(float64)
	return value, ok
}

func mapField(fields map[string]any, key string) map[string]any {
	value, ok := fields[key].(map[string]any)
	if !ok {
		return nil
	}
	return value
}
