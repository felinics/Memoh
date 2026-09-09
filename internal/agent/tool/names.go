package tools

import (
	"github.com/felinics/memoh/internal/agent/tool/internal/toolname"
)

// ToolName identifies a built-in Memoh agent tool.
type ToolName = toolname.Name

func ToolRead() ToolName       { return toolname.ToolRead() }
func ToolWrite() ToolName      { return toolname.ToolWrite() }
func ToolList() ToolName       { return toolname.ToolList() }
func ToolEdit() ToolName       { return toolname.ToolEdit() }
func ToolExec() ToolName       { return toolname.ToolExec() }
func ToolApplyPatch() ToolName { return toolname.ToolApplyPatch() }
func ToolListExecutionLocations() ToolName {
	return toolname.ToolListExecutionLocations()
}
func ToolListBackground() ToolName      { return toolname.ToolListBackground() }
func ToolGetBackgroundStatus() ToolName { return toolname.ToolGetBackgroundStatus() }
func ToolKillBackground() ToolName      { return toolname.ToolKillBackground() }
func ToolWait() ToolName                { return toolname.ToolWait() }
func ToolWaitUntil() ToolName           { return toolname.ToolWaitUntil() }

func ToolSend() ToolName  { return toolname.ToolSend() }
func ToolReact() ToolName { return toolname.ToolReact() }
func ToolSpeak() ToolName { return toolname.ToolSpeak() }

func ToolGetContacts() ToolName    { return toolname.ToolGetContacts() }
func ToolListSessions() ToolName   { return toolname.ToolListSessions() }
func ToolGetMessages() ToolName    { return toolname.ToolGetMessages() }
func ToolSearchMessages() ToolName { return toolname.ToolSearchMessages() }
func ToolSearchMemory() ToolName   { return toolname.ToolSearchMemory() }
func ToolListSkills() ToolName     { return toolname.ToolListSkills() }
func ToolUseSkill() ToolName       { return toolname.ToolUseSkill() }
func ToolSpawnAgent() ToolName     { return toolname.ToolSpawnAgent() }
func ToolSendMessage() ToolName    { return toolname.ToolSendMessage() }
func ToolListAgents() ToolName     { return toolname.ToolListAgents() }
func ToolListModels() ToolName     { return toolname.ToolListModels() }

func ToolListWorkdirs() ToolName  { return toolname.ToolListWorkdirs() }
func ToolListACPAgents() ToolName { return toolname.ToolListACPAgents() }

func ToolListSchedule() ToolName   { return toolname.ToolListSchedule() }
func ToolGetSchedule() ToolName    { return toolname.ToolGetSchedule() }
func ToolCreateSchedule() ToolName { return toolname.ToolCreateSchedule() }
func ToolUpdateSchedule() ToolName { return toolname.ToolUpdateSchedule() }
func ToolDeleteSchedule() ToolName { return toolname.ToolDeleteSchedule() }

func ToolBrowserAction() ToolName        { return toolname.ToolBrowserAction() }
func ToolBrowserObserve() ToolName       { return toolname.ToolBrowserObserve() }
func ToolComputerObserve() ToolName      { return toolname.ToolComputerObserve() }
func ToolComputerAction() ToolName       { return toolname.ToolComputerAction() }
func ToolBrowserRemoteSession() ToolName { return toolname.ToolBrowserRemoteSession() }

func ToolWebSearch() ToolName       { return toolname.ToolWebSearch() }
func ToolWebFetch() ToolName        { return toolname.ToolWebFetch() }
func ToolGenerateImage() ToolName   { return toolname.ToolGenerateImage() }
func ToolGenerateVideo() ToolName   { return toolname.ToolGenerateVideo() }
func ToolTranscribeAudio() ToolName { return toolname.ToolTranscribeAudio() }
func ToolAskUser() ToolName         { return toolname.ToolAskUser() }

func ToolListEmailAccounts() ToolName { return toolname.ToolListEmailAccounts() }
func ToolSendEmail() ToolName         { return toolname.ToolSendEmail() }
func ToolListEmail() ToolName         { return toolname.ToolListEmail() }
func ToolReadEmail() ToolName         { return toolname.ToolReadEmail() }

func toolRef(name ToolName) string {
	return "`" + name.String() + "`"
}

func IsBuiltInToolName(name string) bool {
	_, ok := lookupBuiltInToolName(name)
	return ok
}

func BuiltInToolNames() []ToolName {
	return toolname.All()
}

func lookupBuiltInToolName(name string) (ToolName, bool) {
	return toolname.Lookup(name)
}
