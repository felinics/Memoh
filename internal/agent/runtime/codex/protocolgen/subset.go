package protocolgen

// The generator emits only the v2 subset Memoh actually speaks. Unknown
// methods and variants are tolerated at runtime by design (decoded to raw
// envelopes), so growing this list later is purely additive: add the method,
// regenerate, and the new typed params appear.
//
// The two v1 legacy server requests (applyPatchApproval, execCommandApproval)
// are deliberately absent: the runtime is v2-only.

// clientMethod is a request Memoh sends to the app-server.
type clientMethod struct {
	Method string
	// Response names the response definition. Empty means the method is
	// handled outside the generated code (only `initialize`, whose response
	// type is version-independent bootstrap and lives in the protocol
	// package's hand-written core).
	Response string
}

var clientMethods = []clientMethod{
	{Method: "initialize"},
	{Method: "thread/start", Response: "ThreadStartResponse"},
	{Method: "thread/resume", Response: "ThreadResumeResponse"},
	{Method: "thread/fork", Response: "ThreadForkResponse"},
	{Method: "thread/read", Response: "ThreadReadResponse"},
	{Method: "thread/compact/start", Response: "ThreadCompactStartResponse"},
	{Method: "turn/start", Response: "TurnStartResponse"},
	{Method: "turn/steer", Response: "TurnSteerResponse"},
	{Method: "turn/interrupt", Response: "TurnInterruptResponse"},
	{Method: "model/list", Response: "ModelListResponse"},
	{Method: "account/read", Response: "GetAccountResponse"},
	{Method: "account/rateLimits/read", Response: "GetAccountRateLimitsResponse"},
	{Method: "account/login/start", Response: "LoginAccountResponse"},
	{Method: "account/login/cancel", Response: "CancelLoginAccountResponse"},
	{Method: "account/logout", Response: "LogoutAccountResponse"},
}

// serverRequestMethod is a request the app-server sends to Memoh and waits on
// (approvals, elicitation, auth refresh). Responses are standalone documents
// vendored next to the bundle.
type serverRequestMethod struct {
	Method   string
	Response string
}

var serverRequestMethods = []serverRequestMethod{
	{Method: "item/commandExecution/requestApproval", Response: "CommandExecutionRequestApprovalResponse"},
	{Method: "item/fileChange/requestApproval", Response: "FileChangeRequestApprovalResponse"},
	{Method: "item/permissions/requestApproval", Response: "PermissionsRequestApprovalResponse"},
	{Method: "item/tool/requestUserInput", Response: "ToolRequestUserInputResponse"},
	{Method: "mcpServer/elicitation/request", Response: "McpServerElicitationRequestResponse"},
	{Method: "account/chatgptAuthTokens/refresh", Response: "ChatgptAuthTokensRefreshResponse"},
}

// serverNotifications are the notifications Memoh decodes into typed params.
// Anything not listed still surfaces as a raw envelope.
var serverNotifications = []string{
	"error",
	"warning",
	"configWarning",
	"deprecationNotice",
	"thread/started",
	"thread/status/changed",
	"thread/tokenUsage/updated",
	"thread/compacted",
	"turn/started",
	"turn/completed",
	"turn/plan/updated",
	"item/started",
	"item/completed",
	"item/agentMessage/delta",
	"item/reasoning/textDelta",
	"item/reasoning/summaryTextDelta",
	"item/reasoning/summaryPartAdded",
	"item/commandExecution/outputDelta",
	"item/fileChange/outputDelta",
	"account/updated",
	"account/login/completed",
	"account/rateLimits/updated",
	"serverRequest/resolved",
}
