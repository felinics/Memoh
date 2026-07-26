// Package catalog owns transport-free email provider descriptors and config
// normalization. Both Server profiles and concrete runtime adapters reuse this
// package so API behavior does not depend on compiling email transports.
package catalog

import (
	"errors"
	"fmt"
	"strings"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

const (
	ProviderGeneric emailport.ProviderName = "generic"
	ProviderGmail   emailport.ProviderName = "gmail"
	ProviderMailgun emailport.ProviderName = "mailgun"

	MailgunInboundModeWebhook = "webhook"
	MailgunInboundModePoll    = "poll"
)

type descriptor struct {
	name      emailport.ProviderName
	meta      emailport.ProviderMeta
	normalize func(map[string]any) (map[string]any, error)
}

func (d descriptor) Type() emailport.ProviderName { return d.name }

func (d descriptor) Meta() emailport.ProviderMeta { return d.meta }

func (d descriptor) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return d.normalize(raw)
}

func Generic() emailport.Adapter {
	return descriptor{
		name: ProviderGeneric,
		meta: emailport.ProviderMeta{
			Provider:    string(ProviderGeneric),
			DisplayName: "Generic (SMTP/IMAP)",
			ConfigSchema: emailport.ConfigSchema{Fields: []emailport.FieldSchema{
				{Key: "username", Type: "string", Title: "Username", Required: true, Example: "user@gmail.com", Order: 1},
				{Key: "password", Type: "secret", Title: "Password", Required: true, Order: 2},
				{Key: "smtp_host", Type: "string", Title: "SMTP Host", Required: true, Example: "smtp.gmail.com", Order: 3},
				{Key: "smtp_port", Type: "number", Title: "SMTP Port", Required: true, Example: 587, Order: 4},
				{Key: "smtp_security", Type: "enum", Title: "SMTP Security", Enum: []string{"tls", "starttls", "none"}, Example: "starttls", Order: 5},
				{Key: "imap_host", Type: "string", Title: "IMAP Host", Required: true, Example: "imap.gmail.com", Order: 6},
				{Key: "imap_port", Type: "number", Title: "IMAP Port", Required: true, Example: 993, Order: 7},
				{Key: "imap_security", Type: "enum", Title: "IMAP Security", Enum: []string{"tls", "starttls", "none"}, Example: "tls", Order: 8},
				{Key: "poll_interval_seconds", Type: "number", Title: "Poll Interval (seconds)", Description: "Fallback poll interval when IDLE is not supported", Example: 300, Order: 9},
			}},
		},
		normalize: normalizeGeneric,
	}
}

func Gmail() emailport.Adapter {
	return descriptor{
		name: ProviderGmail,
		meta: emailport.ProviderMeta{
			Provider:    string(ProviderGmail),
			DisplayName: "Gmail (OAuth2)",
			ConfigSchema: emailport.ConfigSchema{Fields: []emailport.FieldSchema{
				{Key: "email_address", Type: "string", Title: "Gmail Address", Required: true, Example: "you@gmail.com", Order: 1},
			}},
		},
		normalize: normalizeGmail,
	}
}

func Mailgun() emailport.Adapter {
	return descriptor{
		name: ProviderMailgun,
		meta: emailport.ProviderMeta{
			Provider:    string(ProviderMailgun),
			DisplayName: "Mailgun",
			ConfigSchema: emailport.ConfigSchema{Fields: []emailport.FieldSchema{
				{Key: "domain", Type: "string", Title: "Domain", Required: true, Example: "mg.example.com", Order: 1},
				{Key: "api_key", Type: "secret", Title: "API Key", Required: true, Order: 2},
				{Key: "region", Type: "enum", Title: "Region", Enum: []string{"us", "eu"}, Example: "us", Order: 3},
				{Key: "inbound_mode", Type: "enum", Title: "Inbound Mode", Description: "webhook requires public IP; poll does not", Enum: []string{MailgunInboundModeWebhook, MailgunInboundModePoll}, Example: MailgunInboundModePoll, Order: 4},
				{Key: "webhook_signing_key", Type: "secret", Title: "Webhook Signing Key", Description: "Required for webhook mode", Order: 5},
				{Key: "poll_interval_seconds", Type: "number", Title: "Poll Interval (seconds)", Description: "For poll mode (minimum 15)", Example: 30, Order: 6},
			}},
		},
		normalize: normalizeMailgun,
	}
}

func normalizeGeneric(raw map[string]any) (map[string]any, error) {
	for _, key := range []string{"smtp_host", "imap_host", "username", "password"} {
		if value, _ := raw[key].(string); strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is required", key)
		}
	}
	if _, ok := raw["smtp_port"]; !ok {
		raw["smtp_port"] = float64(587)
	}
	if _, ok := raw["imap_port"]; !ok {
		raw["imap_port"] = float64(993)
	}
	if _, ok := raw["smtp_security"]; !ok {
		raw["smtp_security"] = "starttls"
	}
	if _, ok := raw["imap_security"]; !ok {
		raw["imap_security"] = "tls"
	}
	if _, ok := raw["poll_interval_seconds"]; !ok {
		raw["poll_interval_seconds"] = float64(300)
	}
	return raw, nil
}

func normalizeGmail(raw map[string]any) (map[string]any, error) {
	clean := make(map[string]any, len(raw))
	for key, value := range raw {
		if key == "client_id" || key == "client_secret" {
			continue
		}
		clean[key] = value
	}
	if len(clean) == 0 {
		return clean, nil
	}
	if value, _ := clean["email_address"].(string); strings.TrimSpace(value) == "" {
		return nil, errors.New("email_address is required")
	}
	return clean, nil
}

func normalizeMailgun(raw map[string]any) (map[string]any, error) {
	for _, key := range []string{"domain", "api_key"} {
		if value, _ := raw[key].(string); strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is required", key)
		}
	}
	mode, _ := raw["inbound_mode"].(string)
	if mode == "" {
		raw["inbound_mode"] = MailgunInboundModePoll
	}
	if mode == MailgunInboundModeWebhook {
		if value, _ := raw["webhook_signing_key"].(string); strings.TrimSpace(value) == "" {
			return nil, errors.New("webhook_signing_key is required for webhook mode")
		}
	}
	if _, ok := raw["region"]; !ok {
		raw["region"] = "us"
	}
	if _, ok := raw["poll_interval_seconds"]; !ok {
		raw["poll_interval_seconds"] = float64(30)
	}
	return raw, nil
}
