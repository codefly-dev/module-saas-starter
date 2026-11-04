package infra

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/jackc/pgx/v5"
)

// Client returns metadata, including disabled identities. The authority core
// decides whether a client may act; registration itself grants no authority.
func (t coreTx) Client(id string) (domain.Client, error) {
	var c domain.Client
	var revision string
	err := t.q.QueryRow(t.ctx, `SELECT id, name, redirect_uris, revision::text, disabled
		FROM public.authority_core_clients WHERE id=$1`, id).
		Scan(&c.ID, &c.Name, &c.RedirectURIs, &revision, &c.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, coreMissing("client", id)
	}
	if err != nil {
		return c, err
	}
	c.Revision, err = strconv.ParseUint(revision, 10, 64)
	if err != nil {
		return c, fmt.Errorf("client %s: stored revision does not read: %w", id, err)
	}
	return c, nil
}

// PutClient replaces the supplied record in the caller's core transaction. A
// revision is an owner value, not a counter the SQL adapter advances.
func (t coreTx) PutClient(c domain.Client) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	_, err := t.q.Exec(t.ctx, `INSERT INTO public.authority_core_clients
		(id, name, redirect_uris, revision, disabled) VALUES ($1,$2,$3,$4::text::numeric,$5)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name,
		redirect_uris=EXCLUDED.redirect_uris, revision=EXCLUDED.revision, disabled=EXCLUDED.disabled`,
		c.ID, c.Name, c.RedirectURIs, strconv.FormatUint(c.Revision, 10), c.Disabled)
	return err
}
