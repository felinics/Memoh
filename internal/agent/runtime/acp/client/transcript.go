package client

import "github.com/felinics/memoh/internal/agent/runtime/external"

type TranscriptRecorder = external.TranscriptRecorder

var (
	NewTranscriptRecorder = external.NewTranscriptRecorder
	TranscriptFromEvents  = external.TranscriptFromEvents
	AppendTranscriptText  = external.AppendTranscriptText
)
