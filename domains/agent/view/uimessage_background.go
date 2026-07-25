package view

import "strings"

// ApplyBackgroundTaskSnapshots overlays live background-task state onto
// converted UI turns. This keeps persisted background_started tool results
// accurate after a page reload.
func ApplyBackgroundTaskSnapshots(turns []UITurn, tasks []UIBackgroundTask) {
	if len(turns) == 0 {
		return
	}

	byID := make(map[string]UIBackgroundTask, len(tasks))
	for _, task := range tasks {
		if taskID := strings.TrimSpace(task.TaskID); taskID != "" {
			byID[taskID] = task
		}
	}
	for turnIdx := range turns {
		if turns[turnIdx].Role != "assistant" {
			continue
		}
		for messageIdx := range turns[turnIdx].Messages {
			//nolint:gosec // G602 false positive: messageIdx comes from range over this slice
			message := &turns[turnIdx].Messages[messageIdx]
			if message.Type != UIMessageTool || message.Background == nil {
				continue
			}
			task, ok := byID[strings.TrimSpace(message.Background.TaskID)]
			if !ok {
				closeMissingBackgroundTaskSnapshot(message)
				continue
			}
			mergeBackgroundTaskIntoTool(message, task)
		}
	}
}

func closeMissingBackgroundTaskSnapshot(message *UIMessage) {
	if message == nil || !isBackgroundToolStillRunning(*message) {
		return
	}
	mergeBackgroundTaskIntoTool(message, UIBackgroundTask{
		TaskID: message.Background.TaskID,
		Status: "unknown",
	})
}
