package email

import (
	"context"
	"fmt"
	"strings"
)

// Template represents a named email template with subject/body patterns.
type Template struct {
	Name            string
	SubjectTemplate string
	HTMLTemplate    string
	TextTemplate    string
}

// TemplateStore abstracts persistence for email templates.
type TemplateStore interface {
	GetTemplate(ctx context.Context, name string) (*Template, error)
}

// TemplateSender wraps a Sender and a TemplateStore to send templated emails.
type TemplateSender struct {
	sender Sender
	store  TemplateStore
}

// NewTemplateSender creates a TemplateSender backed by the given sender and template store.
func NewTemplateSender(sender Sender, store TemplateStore) *TemplateSender {
	return &TemplateSender{sender: sender, store: store}
}

// SendTemplate loads a template by name, applies variable substitution,
// and sends it via the underlying Sender. Returns the provider message id.
func (ts *TemplateSender) SendTemplate(ctx context.Context, templateName, to string, vars map[string]string) (string, error) {
	tmpl, err := ts.store.GetTemplate(ctx, templateName)
	if err != nil {
		return "", fmt.Errorf("email: load template %q: %w", templateName, err)
	}

	subject := applyVars(tmpl.SubjectTemplate, vars)
	htmlBody := applyVars(tmpl.HTMLTemplate, vars)
	textBody := applyVars(tmpl.TextTemplate, vars)

	msg := &Message{
		To:       []string{to},
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
		Tags:     map[string]string{"template": templateName},
	}

	return ts.sender.Send(ctx, msg)
}

// applyVars replaces each {{key}} in s with the corresponding value from vars.
func applyVars(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}
