package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func TestSessionSupportsLegacyModelProtocol(t *testing.T) {
	t.Parallel()

	clientToPeerReader, clientToPeerWriter := io.Pipe()
	peerToClientReader, peerToClientWriter := io.Pipe()
	stopPeer := make(chan struct{})
	t.Cleanup(func() {
		close(stopPeer)
		_ = clientToPeerReader.Close()
		_ = clientToPeerWriter.Close()
		_ = peerToClientReader.Close()
		_ = peerToClientWriter.Close()
	})

	peerErr := make(chan error, 1)
	selected := make(chan legacySetSessionModelRequest, 1)
	go func() {
		decoder := json.NewDecoder(clientToPeerReader)
		encoder := json.NewEncoder(peerToClientWriter)
		for requestIndex := 0; requestIndex < 2; requestIndex++ {
			var request rawACPRequest
			if err := decoder.Decode(&request); err != nil {
				peerErr <- fmt.Errorf("decode request: %w", err)
				return
			}
			switch request.Method {
			case acp.AgentMethodSessionNew:
				description := "Hermes provider model"
				if err := encoder.Encode(rawACPResponse{
					JSONRPC: "2.0",
					ID:      request.ID,
					Result: map[string]any{
						"sessionId": "hermes-session",
						"models": map[string]any{
							"currentModelId": "openrouter:hermes-3",
							"availableModels": []map[string]any{
								{
									"modelId":     "openrouter:hermes-3",
									"name":        "Hermes 3",
									"description": description,
								},
								{
									"modelId": "nous:hermes-4",
									"name":    "Hermes 4",
								},
							},
						},
					},
				}); err != nil {
					peerErr <- fmt.Errorf("encode session response: %w", err)
					return
				}
			case legacyAgentMethodSessionSetModel:
				var params legacySetSessionModelRequest
				if err := json.Unmarshal(request.Params, &params); err != nil {
					peerErr <- fmt.Errorf("decode set-model params: %w", err)
					return
				}
				selected <- params
				if err := encoder.Encode(rawACPResponse{
					JSONRPC: "2.0",
					ID:      request.ID,
					Result:  map[string]any{},
				}); err != nil {
					peerErr <- fmt.Errorf("encode set-model response: %w", err)
					return
				}
			default:
				peerErr <- fmt.Errorf("unexpected ACP method %q", request.Method)
				return
			}
		}
		<-stopPeer
	}()

	conn := newClientConnection(&clientCallbacks{}, clientToPeerWriter, peerToClientReader)
	response, err := conn.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if response.SessionId != "hermes-session" || response.Models == nil {
		t.Fatalf("NewSession() = %#v, want legacy Hermes model state", response)
	}

	sess := &Session{conn: conn, sessionID: response.SessionId}
	sess.replaceConfigOptions(response.SessionId, response.ConfigOptions)
	sess.installLegacyModels(response.Models)
	state := sess.ModelState()
	if !state.Supported || state.CurrentModelID != "openrouter:hermes-3" || len(state.Available) != 2 {
		t.Fatalf("ModelState() = %#v, want normalized legacy models", state)
	}
	if state.Available[0].Description != "Hermes provider model" {
		t.Fatalf("model description = %q", state.Available[0].Description)
	}

	state, err = sess.SetModel(context.Background(), "nous:hermes-4")
	if err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	if state.CurrentModelID != "nous:hermes-4" {
		t.Fatalf("SetModel() state = %#v", state)
	}
	select {
	case params := <-selected:
		if params.SessionID != response.SessionId || params.ModelID != "nous:hermes-4" {
			t.Fatalf("session/set_model params = %#v", params)
		}
	case err := <-peerErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("session/set_model was not received")
	}
}

func TestSessionPrefersConfigOptionModelsOverLegacyModels(t *testing.T) {
	t.Parallel()

	category := acp.SessionConfigOptionCategoryModel
	options := []acp.SessionConfigOption{testSelectOption(
		"model",
		&category,
		"standard-current",
		"standard-current",
		"standard-next",
	)}
	sess := &Session{sessionID: "session-1"}
	sess.replaceConfigOptions(sess.sessionID, options)
	sess.installLegacyModels(&legacySessionModelState{
		CurrentModelID: "legacy-current",
		AvailableModels: []legacyModelInfo{
			{ModelID: "legacy-current", Name: "Legacy"},
		},
	})

	state := sess.ModelState()
	if state.CurrentModelID != "standard-current" || len(state.Available) != 2 {
		t.Fatalf("ModelState() = %#v, want config-option models", state)
	}
	if _, ok := sess.modelSelector.(*configOptionModelSelector); !ok {
		t.Fatalf("model selector = %T, want config-option selector", sess.modelSelector)
	}
}

func TestConfigUpdatesPreserveIndependentLegacyModelState(t *testing.T) {
	t.Parallel()

	sess := &Session{sessionID: "session-1"}
	sess.installLegacyModels(&legacySessionModelState{
		CurrentModelID: "openrouter:hermes-3",
		AvailableModels: []legacyModelInfo{
			{ModelID: "openrouter:hermes-3", Name: "Hermes 3"},
		},
	})
	thoughtLevel := acp.SessionConfigOptionCategoryThoughtLevel
	sess.replaceConfigOptions(sess.sessionID, []acp.SessionConfigOption{
		testSelectOption("thinking", &thoughtLevel, "high", "low", "high"),
	})

	state := sess.ModelState()
	if !state.Supported || state.CurrentModelID != "openrouter:hermes-3" {
		t.Fatalf("ModelState() = %#v, want preserved legacy state", state)
	}
	if !sess.ReasoningState().Supported {
		t.Fatal("reasoning config update was not applied")
	}
}

type rawACPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rawACPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}
