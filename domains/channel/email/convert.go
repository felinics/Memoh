package email

import (
	"errors"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

func mapStoreErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, emailport.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func toPortOutbound(msg OutboundEmail) emailport.OutboundEmail {
	return emailport.OutboundEmail{
		To:      append([]string(nil), msg.To...),
		Subject: msg.Subject,
		Body:    msg.Body,
		HTML:    msg.HTML,
	}
}

func fromPortInbound(mail emailport.InboundEmail) InboundEmail {
	return InboundEmail{
		MessageID:   mail.MessageID,
		From:        mail.From,
		To:          append([]string(nil), mail.To...),
		Subject:     mail.Subject,
		BodyText:    mail.BodyText,
		BodyHTML:    mail.BodyHTML,
		Attachments: append([]any(nil), mail.Attachments...),
		Headers:     mail.Headers,
		ReceivedAt:  mail.ReceivedAt,
	}
}

func fromPortInboundPtr(mail *emailport.InboundEmail) *InboundEmail {
	if mail == nil {
		return nil
	}
	out := fromPortInbound(*mail)
	return &out
}

func fromPortInboundSlice(items []emailport.InboundEmail) []InboundEmail {
	out := make([]InboundEmail, 0, len(items))
	for _, item := range items {
		out = append(out, fromPortInbound(item))
	}
	return out
}

func fromPortMeta(meta emailport.ProviderMeta) ProviderMeta {
	fields := make([]FieldSchema, 0, len(meta.ConfigSchema.Fields))
	for _, field := range meta.ConfigSchema.Fields {
		fields = append(fields, FieldSchema{
			Key:         field.Key,
			Type:        field.Type,
			Title:       field.Title,
			Description: field.Description,
			Required:    field.Required,
			Enum:        append([]string(nil), field.Enum...),
			Example:     field.Example,
			Order:       field.Order,
		})
	}
	return ProviderMeta{
		Provider:     meta.Provider,
		DisplayName:  meta.DisplayName,
		ConfigSchema: ConfigSchema{Fields: fields},
	}
}

func fromPortOAuthToken(token *emailport.OAuthToken) *OAuthToken {
	if token == nil {
		return nil
	}
	return &OAuthToken{
		ProviderID:   token.ProviderID,
		EmailAddress: token.EmailAddress,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt,
		Scope:        token.Scope,
	}
}

func toPortOAuthToken(token OAuthToken) emailport.OAuthToken {
	return emailport.OAuthToken{
		ProviderID:   token.ProviderID,
		EmailAddress: token.EmailAddress,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt,
		Scope:        token.Scope,
	}
}
