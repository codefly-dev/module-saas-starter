package email

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

var templateVariablePattern = regexp.MustCompile(`\{\{[A-Za-z][A-Za-z0-9_]*\}\}`)

// Template represents a named email template with subject/body patterns.
type Template struct {
	Name            string
	Version         int
	SubjectTemplate string
	HTMLTemplate    string
	TextTemplate    string
}

// TemplateStore abstracts persistence for email templates.
type TemplateStore interface {
	GetTemplate(ctx context.Context, name string) (*Template, error)
}

// RenderTemplate loads and renders one immutable message. Rendering is kept
// separate from transport so producers can persist the exact result before a
// worker performs the external side effect.
func RenderTemplate(
	ctx context.Context,
	store TemplateStore,
	from string,
	templateName string,
	to string,
	vars map[string]string,
) (*Message, error) {
	if store == nil {
		return nil, fmt.Errorf("email: template store is required")
	}
	tmpl, err := store.GetTemplate(ctx, templateName)
	if err != nil {
		return nil, fmt.Errorf("email: load template %q: %w", templateName, err)
	}

	subject, err := renderPattern(tmpl.SubjectTemplate, vars, false)
	if err != nil {
		return nil, fmt.Errorf("email: render template %q subject: %w", templateName, err)
	}
	htmlBody, err := renderPattern(tmpl.HTMLTemplate, vars, true)
	if err != nil {
		return nil, fmt.Errorf("email: render template %q HTML body: %w", templateName, err)
	}
	textBody, err := renderPattern(tmpl.TextTemplate, vars, false)
	if err != nil {
		return nil, fmt.Errorf("email: render template %q text body: %w", templateName, err)
	}

	tags := map[string]string{"template": templateName}
	if tmpl.Version > 0 {
		tags["template_version"] = strconv.Itoa(tmpl.Version)
	}
	msg := &Message{
		From:     from,
		To:       []string{to},
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
		Tags:     tags,
	}
	if err := msg.Validate(); err != nil {
		return nil, err
	}
	return msg, nil
}

// renderPattern resolves the starter's deliberately small {{variable}}
// language. It fails closed on malformed or unresolved placeholders. HTML
// fields escape each value at insertion time; subject and text fields retain
// their plain-text representation.
func renderPattern(pattern string, vars map[string]string, escapeHTML bool) (string, error) {
	remainder := templateVariablePattern.ReplaceAllString(pattern, "")
	if strings.Contains(remainder, "{{") || strings.Contains(remainder, "}}") {
		return "", fmt.Errorf("malformed template placeholder")
	}

	var missing string
	rendered := templateVariablePattern.ReplaceAllStringFunc(pattern, func(placeholder string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(placeholder, "{{"), "}}")
		value, ok := vars[key]
		if !ok {
			missing = key
			return ""
		}
		if escapeHTML {
			return html.EscapeString(value)
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("missing variable %q", missing)
	}
	return rendered, nil
}
