package business

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Delegations to a module (migration 48): the host authority interface's
// Delegate. A signed-in person grants one composed module the right to act for
// them beyond any session, on scopes they hold. The module issues from it
// through a binding it declares; each issue re-reads the delegation and what
// its grantor holds then, so it never confers more than the grantor still
// holds. Revoking stamps the row once and advances the grantor's authorization
// revision in the same statement, so every capability already issued from it
// fails its next verification.
//
// This is not a source delegation: that one binds a person's datasource to one
// binding of a module and confers the binding's declared delegation scopes; this
// one names scopes and no source, and any binding of the module may exercise it
// within them.

// ModuleDelegation is one row of module_delegations.
type ModuleDelegation struct {
	ID           string
	OrgID        string
	PrincipalID  string // the grantor, whom every capability issued from it is for
	ModulePrefix string
	Scopes       []ModuleOperationScope
	Purpose      string
	CreatedAt    time.Time
	RevokedAt    *time.Time
	RevokedBy    string
}

// Active reports whether the delegation has not been revoked.
func (d *ModuleDelegation) Active() bool { return d != nil && d.RevokedAt == nil }

var (
	// ErrModuleDelegationNotFound: no delegation by that id that the caller
	// may see.
	ErrModuleDelegationNotFound = errors.New("module delegation not found")
	// ErrModuleDelegationInvalid: a delegation that names no module, no
	// grantor or nothing it grants.
	ErrModuleDelegationInvalid = errors.New("a delegation names its organization, its grantor, the module granted and what it grants")
)

// RecordModuleDelegation records one active delegation in its organization.
// The caller has held what it grants to what the grantor holds.
func (s *Service) RecordModuleDelegation(ctx context.Context, d *ModuleDelegation) error {
	if d == nil || d.OrgID == "" || d.PrincipalID == "" || strings.TrimSpace(d.ModulePrefix) == "" || len(d.Scopes) == 0 {
		return ErrModuleDelegationInvalid
	}
	d.ID = NewIDString()
	return s.store.WithOrgTx(ctx, d.OrgID, func(ctx context.Context) error {
		return s.store.InsertModuleDelegation(ctx, d)
	})
}

// ModuleDelegation reads one delegation by id in orgID, or by id alone under
// the control plane when orgID is empty: a module proving its workload names
// no organization. The caller compares the module or the grantor itself. Nil
// when there is none.
func (s *Service) ModuleDelegation(ctx context.Context, orgID, id string) (*ModuleDelegation, error) {
	var out []*ModuleDelegation
	read := func(ctx context.Context) error {
		var err error
		out, err = s.store.ModuleDelegations(ctx, orgID, []string{id})
		return err
	}
	var err error
	if orgID == "" {
		err = s.store.WithControlPlane(ctx, read)
	} else {
		err = s.store.WithOrgTx(ctx, orgID, read)
	}
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return out[0], nil
}

// ModuleDelegations reads, in orgID, the delegations among ids: the ones an
// actor chain names on its hops. An id that names none is not an error, since
// most hops act under no stored delegation.
func (s *Service) ModuleDelegations(ctx context.Context, orgID string, ids []string) ([]*ModuleDelegation, error) {
	if orgID == "" || len(ids) == 0 {
		return nil, nil
	}
	var out []*ModuleDelegation
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		out, err = s.store.ModuleDelegations(ctx, orgID, ids)
		return err
	})
	return out, err
}

// RevokeModuleDelegation ends one of personID's delegations in orgID and
// returns it as it now stands; revoking one already revoked changes nothing.
// Only its grantor may: anyone else is told there is none
// (ErrModuleDelegationNotFound), as GetDelegation tells them.
//
// A narrowing, so it runs under the policy log like every other revocation:
// witnessed before the row is stamped, the stamp and the receipt committed in
// one transaction.
func (s *Service) RevokeModuleDelegation(ctx context.Context, orgID, personID, id string) (*ModuleDelegation, error) {
	if orgID == "" || personID == "" || id == "" {
		return nil, ErrModuleDelegationNotFound
	}
	var out *ModuleDelegation
	err := s.WithPolicyLoggedNarrowing(ctx, revokeModuleDelegationPolicyLogEntry(personID, orgID, id), func(ctx context.Context) error {
		found, err := s.store.RevokeModuleDelegation(ctx, orgID, personID, id)
		if err != nil {
			return err
		}
		if found == nil {
			return ErrModuleDelegationNotFound
		}
		out = found
		return nil
	})
	return out, err
}
