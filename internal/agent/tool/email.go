package tools

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/email"
)

type EmailProvider struct {
	logger  *slog.Logger
	service *email.Service
	manager email.Runtime
}

func NewEmailProvider(log *slog.Logger, service *email.Service, manager email.Runtime) *EmailProvider {
	return &EmailProvider{
		logger:  log.With(slog.String("tool", "email")),
		service: service,
		manager: manager,
	}
}

func (p *EmailProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	sess := session
	return []sdk.Tool{
		{
			Name: ToolListEmailAccounts().String(), Description: "List the email accounts (provider bindings) configured for this bot, including provider IDs, email addresses, and permissions.",
			Parameters: emptyObjectSchema(),
			Execute: func(ctx *sdk.ToolExecContext, _ any) (any, error) {
				return p.execListAccounts(ctx.Context, sess)
			},
		},
		{
			Name: ToolSendEmail().String(), Description: "Send an email via the bot's configured email provider. Requires write permission.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to":          map[string]any{"type": "string", "description": "Recipient email address(es), comma-separated"},
					"subject":     map[string]any{"type": "string", "description": "Email subject"},
					"body":        map[string]any{"type": "string", "description": "Email body content"},
					"html":        map[string]any{"type": "boolean", "description": "Whether body is HTML (default false)"},
					"provider_id": map[string]any{"type": "string", "description": "Email provider ID to send from (optional, uses default if omitted)"},
				},
				"required": []string{"to", "subject", "body"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execSendEmail(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name: ToolListEmail().String(), Description: "List emails from the mailbox (newest first). Supports pagination. Requires read permission.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"page":        map[string]any{"type": "integer", "description": "Page number, 0-based (default 0 = newest)"},
					"page_size":   map[string]any{"type": "integer", "description": "Emails per page (default 20)"},
					"provider_id": map[string]any{"type": "string", "description": "Email provider ID (optional, uses first readable binding)"},
				},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execListEmails(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name: ToolReadEmail().String(), Description: "Read the full content of an email by its UID. Requires read permission.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"uid":         map[string]any{"type": "integer", "description": "The email UID from email_list results"},
					"provider_id": map[string]any{"type": "string", "description": "Email provider ID (optional, uses first readable binding)"},
				},
				"required": []string{"uid"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execReadEmail(ctx.Context, sess, inputAsMap(input))
			},
		},
	}, nil
}

func (p *EmailProvider) getBindings(ctx context.Context, botID string) ([]email.BindingResponse, error) {
	bindings, err := p.service.ListBindings(ctx, botID)
	if err != nil || len(bindings) == 0 {
		return nil, errors.New("no email binding configured for this bot")
	}
	return bindings, nil
}

func (p *EmailProvider) execListAccounts(ctx context.Context, session SessionContext) (any, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	bindings, err := p.getBindings(ctx, botID)
	if err != nil {
		return nil, err
	}
	accounts := make([]map[string]any, 0, len(bindings))
	for _, b := range bindings {
		accounts = append(accounts, map[string]any{
			"provider_id": b.EmailProviderID, "email_address": b.EmailAddress,
			"can_read": b.CanRead, "can_write": b.CanWrite, "can_delete": b.CanDelete,
		})
	}
	return map[string]any{"accounts": accounts}, nil
}

func (p *EmailProvider) execSendEmail(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	bindings, err := p.getBindings(ctx, botID)
	if err != nil {
		return nil, err
	}
	binding := resolveWriteBinding(bindings, StringArg(args, "provider_id"))
	if binding == nil {
		return nil, errors.New("email write permission denied or provider not found")
	}
	toRaw := StringArg(args, "to")
	subject := StringArg(args, "subject")
	body := StringArg(args, "body")
	isHTML, _, _ := BoolArg(args, "html")
	if toRaw == "" || subject == "" || body == "" {
		return nil, errors.New("to, subject, and body are required")
	}
	var toList []string
	for _, addr := range strings.Split(toRaw, ",") {
		addr = strings.TrimSpace(addr)
		if addr != "" {
			toList = append(toList, addr)
		}
	}
	messageID, err := p.manager.SendEmail(ctx, botID, binding.EmailProviderID, email.OutboundEmail{
		To: toList, Subject: subject, Body: body, HTML: isHTML,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"message_id": messageID, "status": "sent"}, nil
}

func (p *EmailProvider) execListEmails(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	bindings, err := p.getBindings(ctx, botID)
	if err != nil {
		return nil, err
	}
	binding := resolveReadBinding(bindings, StringArg(args, "provider_id"))
	if binding == nil {
		return nil, errors.New("email read permission denied or provider not found")
	}
	providerName, config, err := p.service.ProviderConfig(ctx, binding.EmailProviderID)
	if err != nil {
		return nil, err
	}
	config = ensureProviderID(config, binding.EmailProviderID)
	reader, err := p.service.Registry().GetMailboxReader(providerName)
	if err != nil {
		return nil, errors.New("mailbox listing not supported for this provider")
	}
	page, _, _ := IntArg(args, "page")
	pageSize, _, _ := IntArg(args, "page_size")
	if pageSize <= 0 {
		pageSize = 20
	}
	emails, total, err := reader.ListMailbox(ctx, config, page, pageSize)
	if err != nil {
		return nil, err
	}
	summaries := make([]map[string]any, 0, len(emails))
	for _, item := range emails {
		summaries = append(summaries, map[string]any{
			"uid": item.MessageID, "from": item.From, "subject": item.Subject, "received_at": item.ReceivedAt,
		})
	}
	return map[string]any{"emails": summaries, "total": total, "page": page}, nil
}

func (p *EmailProvider) execReadEmail(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	bindings, err := p.getBindings(ctx, botID)
	if err != nil {
		return nil, err
	}
	binding := resolveReadBinding(bindings, StringArg(args, "provider_id"))
	if binding == nil {
		return nil, errors.New("email read permission denied or provider not found")
	}
	uidRaw, ok, _ := IntArg(args, "uid")
	if !ok || uidRaw <= 0 {
		uidStr := StringArg(args, "uid")
		if uidStr != "" {
			parsed, _ := strconv.Atoi(uidStr)
			uidRaw = parsed
		}
	}
	if uidRaw <= 0 {
		return nil, errors.New("uid is required")
	}
	if uidRaw > math.MaxUint32 {
		return nil, errors.New("uid out of range")
	}
	providerName, config, err := p.service.ProviderConfig(ctx, binding.EmailProviderID)
	if err != nil {
		return nil, err
	}
	config = ensureProviderID(config, binding.EmailProviderID)
	reader, err := p.service.Registry().GetMailboxReader(providerName)
	if err != nil {
		return nil, errors.New("mailbox reading not supported for this provider")
	}
	item, err := reader.ReadMailbox(ctx, config, uint32(uidRaw)) //nolint:gosec // bounds checked above
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"uid": item.MessageID, "from": item.From, "to": item.To,
		"subject": item.Subject, "body": item.BodyText, "received_at": item.ReceivedAt,
	}, nil
}

func resolveReadBinding(bindings []email.BindingResponse, providerID string) *email.BindingResponse {
	for i := range bindings {
		if !bindings[i].CanRead {
			continue
		}
		if providerID == "" || bindings[i].EmailProviderID == providerID {
			return &bindings[i]
		}
	}
	return nil
}

func resolveWriteBinding(bindings []email.BindingResponse, providerID string) *email.BindingResponse {
	for i := range bindings {
		if !bindings[i].CanWrite {
			continue
		}
		if providerID == "" || bindings[i].EmailProviderID == providerID {
			return &bindings[i]
		}
	}
	return nil
}

func ensureProviderID(config map[string]any, providerID string) map[string]any {
	if config == nil {
		config = make(map[string]any)
	} else {
		copied := make(map[string]any, len(config)+1)
		for k, v := range config {
			copied[k] = v
		}
		config = copied
	}
	config["_provider_id"] = providerID
	return config
}
