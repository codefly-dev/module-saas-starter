package infra

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
	"accounts/pkg/email"
)

// PostgresTemplateStore implements email.TemplateStore backed by Postgres.
type PostgresTemplateStore struct {
	store *PostgresStore
}

// Compile-time check.
var _ email.TemplateStore = (*PostgresTemplateStore)(nil)

// NewPostgresTemplateStore creates a template store that shares a
// PostgresStore's connection pool.
func NewPostgresTemplateStore(store *PostgresStore) *PostgresTemplateStore {
	return &PostgresTemplateStore{store: store}
}

func (s *PostgresTemplateStore) GetTemplate(ctx context.Context, name string) (*email.Template, error) {
	q := s.store.getQueryExecutor(ctx)

	row := q.QueryRow(ctx, `
		SELECT name, version, subject_template, html_template, text_template
		FROM email_templates WHERE name = $1`, name)

	var tmpl email.Template
	var htmlTemplate, textTemplate *string

	err := row.Scan(&tmpl.Name, &tmpl.Version, &tmpl.SubjectTemplate, &htmlTemplate, &textTemplate)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(errors.New("email template not found"), business.ErrTypeNotFound)
		}
		return nil, err
	}

	if htmlTemplate != nil {
		tmpl.HTMLTemplate = *htmlTemplate
	}
	if textTemplate != nil {
		tmpl.TextTemplate = *textTemplate
	}

	return &tmpl, nil
}
