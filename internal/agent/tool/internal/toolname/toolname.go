package toolname

import (
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
)

// Name identifies a built-in Memoh agent tool. Its raw value is intentionally
// not constructible outside this package, so callers must use the exported
// accessor functions instead of local string conversions.
type Name struct {
	value string
}

func newName(value string) Name {
	return Name{value: value}
}

func (n Name) String() string {
	return n.value
}

func (n Name) IsZero() bool {
	return n.value == ""
}

func ToolRead() Name       { return newName("read") }
func ToolWrite() Name      { return newName("write") }
func ToolList() Name       { return newName("list") }
func ToolEdit() Name       { return newName("edit") }
func ToolExec() Name       { return newName("exec") }
func ToolApplyPatch() Name { return newName("apply_patch") }
func ToolListExecutionLocations() Name {
	return newName("list_execution_locations")
}
func ToolListBackground() Name      { return newName("list_background") }
func ToolGetBackgroundStatus() Name { return newName("get_background_status") }
func ToolKillBackground() Name      { return newName("kill_background") }
func ToolWait() Name                { return newName("wait") }
func ToolWaitUntil() Name           { return newName("wait_until") }

func ToolSend() Name  { return newName("send") }
func ToolReact() Name { return newName("react") }
func ToolSpeak() Name { return newName("speak") }

func ToolGetContacts() Name    { return newName("get_contacts") }
func ToolListSessions() Name   { return newName("list_sessions") }
func ToolGetMessages() Name    { return newName("get_messages") }
func ToolSearchMessages() Name { return newName("search_messages") }
func ToolSearchMemory() Name   { return newName(memprovider.ToolSearchMemory) }
func ToolListSkills() Name     { return newName("list_skills") }
func ToolUseSkill() Name       { return newName("use_skill") }
func ToolSpawnAgent() Name     { return newName("spawn_agent") }
func ToolSendMessage() Name    { return newName("send_message") }
func ToolListAgents() Name     { return newName("list_agents") }
func ToolListModels() Name     { return newName("list_models") }

func ToolListWorkdirs() Name  { return newName("list_workdirs") }
func ToolListACPAgents() Name { return newName("list_acp_agents") }

func ToolListSchedule() Name   { return newName("list_schedule") }
func ToolGetSchedule() Name    { return newName("get_schedule") }
func ToolCreateSchedule() Name { return newName("create_schedule") }
func ToolUpdateSchedule() Name { return newName("update_schedule") }
func ToolDeleteSchedule() Name { return newName("delete_schedule") }

func ToolBrowserAction() Name        { return newName("browser_action") }
func ToolBrowserObserve() Name       { return newName("browser_observe") }
func ToolComputerObserve() Name      { return newName("computer_observe") }
func ToolComputerAction() Name       { return newName("computer_action") }
func ToolBrowserRemoteSession() Name { return newName("browser_remote_session") }

func ToolWebSearch() Name       { return newName("web_search") }
func ToolWebFetch() Name        { return newName("web_fetch") }
func ToolGenerateImage() Name   { return newName("generate_image") }
func ToolGenerateVideo() Name   { return newName("generate_video") }
func ToolTranscribeAudio() Name { return newName("transcribe_audio") }
func ToolAskUser() Name         { return newName(userinput.ToolNameAskUser) }

func ToolListEmailAccounts() Name { return newName("list_email_accounts") }
func ToolSendEmail() Name         { return newName("send_email") }
func ToolListEmail() Name         { return newName("list_email") }
func ToolReadEmail() Name         { return newName("read_email") }

var all = []Name{
	ToolRead(), ToolWrite(), ToolList(), ToolEdit(), ToolExec(), ToolApplyPatch(), ToolListExecutionLocations(), ToolListBackground(), ToolGetBackgroundStatus(), ToolKillBackground(), ToolWait(), ToolWaitUntil(),
	ToolSend(), ToolReact(), ToolSpeak(),
	ToolGetContacts(), ToolListSessions(), ToolGetMessages(), ToolSearchMessages(), ToolSearchMemory(), ToolListSkills(), ToolUseSkill(), ToolSpawnAgent(), ToolSendMessage(), ToolListAgents(), ToolListModels(),
	ToolListWorkdirs(), ToolListACPAgents(),
	ToolListSchedule(), ToolGetSchedule(), ToolCreateSchedule(), ToolUpdateSchedule(), ToolDeleteSchedule(),
	ToolBrowserAction(), ToolBrowserObserve(), ToolComputerObserve(), ToolComputerAction(), ToolBrowserRemoteSession(),
	ToolWebSearch(), ToolWebFetch(), ToolGenerateImage(), ToolGenerateVideo(), ToolTranscribeAudio(), ToolAskUser(),
	ToolListEmailAccounts(), ToolSendEmail(), ToolListEmail(), ToolReadEmail(),
}

// All returns the complete built-in Memoh tool catalog.
func All() []Name {
	return append([]Name(nil), all...)
}

// Lookup resolves a raw protocol tool name to its built-in catalog value.
func Lookup(raw string) (Name, bool) {
	for _, name := range all {
		if name.String() == raw {
			return name, true
		}
	}
	return Name{}, false
}
