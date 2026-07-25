package wecom

import "testing"

func TestBuildFrameAndDecodeBody(t *testing.T) {
	type body struct {
		BotID string `json:"bot_id"`
	}
	frame, err := BuildFrame(WSCmdSubscribe, "req-1", body{BotID: "bot123"})
	if err != nil {
		t.Fatalf("BuildFrame error = %v", err)
	}
	var decoded body
	if err := frame.DecodeBody(&decoded); err != nil {
		t.Fatalf("DecodeBody error = %v", err)
	}
	if decoded.BotID != "bot123" {
		t.Fatalf("unexpected decoded body: %+v", decoded)
	}
}

func TestAuthCredentialsValidate(t *testing.T) {
	if err := (AuthCredentials{}).Validate(); err == nil {
		t.Fatal("expected validation error for empty credentials")
	}
	if err := (AuthCredentials{BotID: "id", Credential: "sec"}).Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
