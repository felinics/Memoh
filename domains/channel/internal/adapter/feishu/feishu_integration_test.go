package feishu

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/channel/gateway"
)

// TestFeishuGateway_Integration runs Feishu channel integration test.
// Required env: FEISHU_APP_ID, FEISHU_APP_SECRET.
// Optional: FEISHU_ENCRYPT_KEY, FEISHU_VERIFICATION_TOKEN.
func TestFeishuGateway_Integration(t *testing.T) {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")

	if appID == "" || appSecret == "" {
		t.Skip("skipping integration test: FEISHU_APP_ID or FEISHU_APP_SECRET not set")
	}

	encryptKey := os.Getenv("FEISHU_ENCRYPT_KEY")
	verificationToken := os.Getenv("FEISHU_VERIFICATION_TOKEN")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	adapter := NewFeishuAdapter(logger)

	cfg := gateway.ChannelConfig{
		ID: "integration-test-bot",
		Credentials: map[string]any{
			"app_id":             appID,
			"app_secret":         appSecret,
			"encrypt_key":        encryptKey,
			"verification_token": verificationToken,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	receivedChan := make(chan gateway.InboundMessage, 1)

	handler := func(ctx context.Context, c gateway.ChannelConfig, msg gateway.InboundMessage) error {
		plainText := msg.Message.PlainText()
		logger.InfoContext(ctx, "received message in test",
			slog.String("text", plainText),
			slog.String("user_id", msg.Sender.Attribute("user_id")),
			slog.String("route_key", msg.RoutingKey()))

		select {
		case receivedChan <- msg:
		default:
		}

		reply := gateway.OutboundMessage{
			Target: msg.ReplyTarget,
			Message: gateway.Message{
				Text: fmt.Sprintf("【Memoh 集成测试】已收到消息: %s\n测试时间: %s", plainText, time.Now().Format("15:04:05")),
			},
		}

		if err := adapter.Send(ctx, c, gateway.PreparedOutboundMessage{
			Target: reply.Target,
			Message: gateway.PreparedMessage{
				Message: reply.Message,
			},
		}); err != nil {
			return fmt.Errorf("failed to send reply: %w", err)
		}

		go func() {
			time.Sleep(1 * time.Second)
			pushMsg := gateway.OutboundMessage{
				Target: msg.ReplyTarget,
				Message: gateway.Message{
					Text: "【Memoh 集成测试】主动推送验证成功。",
				},
			}
			pushCtx, pushCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer pushCancel()
			_ = adapter.Send(pushCtx, c, gateway.PreparedOutboundMessage{
				Target: pushMsg.Target,
				Message: gateway.PreparedMessage{
					Message: pushMsg.Message,
				},
			})
		}()

		return nil
	}

	logger.Info("starting Feishu adapter", slog.String("app_id", appID))
	runner, err := adapter.Connect(ctx, cfg, handler)
	if err != nil {
		t.Fatalf("adapter connect failed: %v", err)
	}
	defer func() {
		_ = runner.Stop(context.Background())
	}()

	fmt.Println("==================================================================")
	fmt.Println("Feishu integration test ready. Send a message in Feishu client to verify.")
	fmt.Println("Test ends on first message received or 10 min timeout.")
	fmt.Println("==================================================================")

	select {
	case msg := <-receivedChan:
		logger.Info("integration test passed", slog.String("received_text", msg.Message.PlainText()))
		time.Sleep(2 * time.Second)
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			t.Log("test timed out")
		}
	}
}

// TestFeishuDiscoverSelf_Integration verifies the bot info API call.
// Required env: FEISHU_APP_ID, FEISHU_APP_SECRET.
func TestFeishuDiscoverSelf_Integration(t *testing.T) {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		t.Skip("skipping integration test: FEISHU_APP_ID or FEISHU_APP_SECRET not set")
	}
	adapter := NewFeishuAdapter(nil)
	credentials := map[string]any{
		"app_id":     appID,
		"app_secret": appSecret,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	identity, extID, err := adapter.DiscoverSelf(ctx, credentials)
	if err != nil {
		t.Fatalf("discover self failed: %v", err)
	}
	openID, _ := identity["open_id"].(string)
	if openID == "" {
		t.Fatalf("expected non-empty open_id")
	}
	if extID != openID {
		t.Fatalf("expected external_id=%s, got %s", openID, extID)
	}
	t.Logf("bot identity: %+v", identity)
}
