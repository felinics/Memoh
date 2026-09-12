package tools

// UIOutputMetadataKey is a reserved top-level key in a tool's output map for
// payloads that exist only for the chat UI — e.g. the edit tool's context
// diff. The native runtime strips it before the SDK records the output, so
// the model never sees it: not in the current turn, not in rebuilt history.
// Everything a tool returns outside this key is model-visible; never put
// content the model should keep into it.
const UIOutputMetadataKey = "_ui"
