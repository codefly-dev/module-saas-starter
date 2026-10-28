//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/scope"
	"github.com/codefly-dev/authority/libraries/authority-core/go/storetest"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAuthorityClientMetadataSurvivesSQLReplacement(t *testing.T) {
	state := testStore.AuthorityCore()
	id := "client:" + uuid.NewString()
	read := func() domain.Client {
		var got domain.Client
		require.NoError(t, state.Read(testCtx, func(tx authority.Tx) (err error) {
			got, err = tx.Client(id)
			return err
		}))
		return got
	}
	err := state.Read(testCtx, func(tx authority.Tx) error { _, err := tx.Client(id); return err })
	require.ErrorIs(t, err, domain.ErrNotFound)
	for _, want := range []domain.Client{
		{ID: id, Name: "Example Client", RedirectURIs: []string{"https://example.com/callback"}, Revision: math.MaxUint64, Disabled: true},
		{ID: id, Name: "Replacement Client", RedirectURIs: []string{"https://example.org/return", "http://127.0.0.1/callback"}, Revision: 1},
		{ID: id, RedirectURIs: []string{}, Revision: 0},
		{ID: id, RedirectURIs: nil, Revision: 0},
	} {
		require.NoError(t, state.Write(testCtx, func(tx authority.Tx) error { return tx.PutClient(want) }))
		require.Equal(t, want, read()) // Disabled rows remain readable; zero/false replace old values.
		if len(want.RedirectURIs) > 0 {
			got := read()
			got.RedirectURIs[0] = "https://changed.example.com/callback"
			require.Equal(t, want, read(), "a returned slice cannot mutate persisted metadata")
			want.RedirectURIs[0] = "https://input.example.com/callback"
			require.NotEqual(t, want, read(), "a caller's original slice cannot mutate the stored row")
		}
	}
	want := read()
	changed := want
	changed.Name, changed.Disabled = "Uncommitted", true
	require.Error(t, state.Read(testCtx, func(tx authority.Tx) error { return tx.PutClient(changed) }))
	require.Equal(t, want, read())
	rollback := errors.New("rollback client replacement")
	err = state.Write(testCtx, func(tx authority.Tx) error {
		if err := tx.PutClient(changed); err != nil {
			return err
		}
		fresh := changed
		fresh.ID = id + "-rolled-back"
		if err := tx.PutClient(fresh); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	require.Equal(t, want, read())
	err = state.Read(testCtx, func(tx authority.Tx) error { _, err := tx.Client(id + "-rolled-back"); return err })
	require.ErrorIs(t, err, domain.ErrNotFound)
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	err = state.Read(ctx, func(tx authority.Tx) error {
		cancel()
		_, err := tx.Client(id)
		return err
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, domain.ErrNotFound, "a failed SQL read is not absence")
}

func TestAuthorityClientDelegationSurvivesSQLReadListAndRevoke(t *testing.T) {
	org, person := uuid.NewString(), uuid.NewString()
	seedAuthorityIdentity(t, storetest.Identity{
		Organizations: []domain.Organization{{ID: org, Name: "Example Organization"}},
		Members:       []storetest.Member{{Organization: org, Person: person, Administrator: true}},
	})
	state := testStore.AuthorityCore()
	d := domain.Delegation{
		ID: uuid.NewString(), Organization: org, Grantor: person, Purpose: "Client access",
		ClientID: "client:" + uuid.NewString(), Audience: "example-principal", Resource: "https://example.com/v1/resource/mcp?exact=1",
		Scopes:    []*basev0.WorkScopeV1{{ResourceKind: "record", Actions: []string{"read"}, ResourceIds: []string{"one"}}},
		ExpiresAt: time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour),
	}
	check := func(got domain.Delegation) {
		require.True(t, scope.Same(d.Scopes, got.Scopes))
		require.True(t, d.ExpiresAt.Equal(got.ExpiresAt))
		got.Scopes, got.ExpiresAt = d.Scopes, d.ExpiresAt
		require.Equal(t, d, got)
	}
	for _, revoked := range []bool{false, true, false} {
		d.Revoked = revoked
		require.NoError(t, state.Write(testCtx, func(tx authority.Tx) error { return tx.PutDelegation(d) }))
		require.NoError(t, state.Read(testCtx, func(tx authority.Tx) error {
			got, err := tx.Delegation(d.ID)
			if err != nil {
				return err
			}
			check(got)
			all, err := tx.Delegations()
			if err != nil {
				return err
			}
			found := false
			for _, row := range all {
				if row.ID == d.ID {
					check(row)
					found = true
				}
			}
			require.True(t, found)
			return nil
		}))
	}
	// Replacement persists the supplied arm, including clearing all client fields.
	d.Module, d.ClientID, d.Audience, d.Resource = "example-module", "", "", ""
	require.NoError(t, state.Write(testCtx, func(tx authority.Tx) error { return tx.PutDelegation(d) }))
	require.NoError(t, state.Read(testCtx, func(tx authority.Tx) error {
		got, err := tx.Delegation(d.ID)
		if err == nil {
			check(got)
		}
		return err
	}))
}
