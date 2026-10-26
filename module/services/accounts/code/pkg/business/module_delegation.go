package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Delegations to a module (migration 51): the host authority interface's
// Delegate. A signed-in person grants one composed module the right to act for
// them beyond any session, on scopes they hold. The module issues from it
// through a binding it declares; each issue re-reads the delegation and what
// its grantor holds then, so it never confers more than the grantor still
// holds. Revoking stamps the row once; every capability issued under it is
// sealed to it, so each fails its next verification, and nothing else the
// grantor holds moves.
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

// ModuleDelegationPurposeLimit is the longest purpose a delegation records,
// the bound migration 51 holds the column to.
const ModuleDelegationPurposeLimit = 1024

var (
	// ErrModuleDelegationNotFound: no delegation by that id that the caller
	// may see.
	ErrModuleDelegationNotFound = errors.New("module delegation not found")
	// ErrModuleDelegationInvalid: a delegation that names no module, no
	// grantor or nothing it grants.
	ErrModuleDelegationInvalid = errors.New("a delegation names its organization, its grantor, the module granted and what it grants")
	// ErrModuleDelegationPurposeTooLong: a purpose past
	// ModuleDelegationPurposeLimit characters.
	ErrModuleDelegationPurposeTooLong = fmt.Errorf("a delegation's purpose is at most %d characters", ModuleDelegationPurposeLimit)
	// ErrModuleDelegationUnrevocable: this host cannot witness a revocation
	// (no policy log is wired), so a delegation recorded here could never be
	// ended. It records none: fail closed.
	ErrModuleDelegationUnrevocable = errors.New("this host cannot witness a revocation, so it records no delegation")
)

// ModuleDelegationsRevocable reports whether this host can end a delegation it
// records: a revocation is a narrowing, and a narrowing needs the policy log.
func (s *Service) ModuleDelegationsRevocable() bool {
	return s.policyLog != nil && s.policyLogStore != nil
}

// RecordModuleDelegation records one active delegation in its organization,
// and its grant on the audit spine, in one transaction. The caller has held
// what it grants to what the grantor holds. A host that could not revoke it
// records nothing.
func (s *Service) RecordModuleDelegation(ctx context.Context, d *ModuleDelegation) error {
	if d == nil || d.OrgID == "" || d.PrincipalID == "" || strings.TrimSpace(d.ModulePrefix) == "" || len(d.Scopes) == 0 {
		return ErrModuleDelegationInvalid
	}
	if utf8.RuneCountInString(d.Purpose) > ModuleDelegationPurposeLimit {
		return ErrModuleDelegationPurposeTooLong
	}
	if !s.ModuleDelegationsRevocable() {
		return ErrModuleDelegationUnrevocable
	}
	d.ID = NewIDString()
	return s.store.WithOrgTx(ctx, d.OrgID, func(ctx context.Context) error {
		if err := s.store.InsertModuleDelegation(ctx, d); err != nil {
			return err
		}
		return s.emitTx(ctx, d.PrincipalID, "user", EventModuleDelegationGranted, "module_delegation", d.ID, d.OrgID, moduleDelegationPayload(d))
	})
}

func moduleDelegationPayload(d *ModuleDelegation) map[string]any {
	scopes := make([]string, 0, len(d.Scopes))
	for _, scope := range d.Scopes {
		entry := scope.ResourceKind + ":" + strings.Join(scope.Actions, ",")
		if len(scope.ResourceIDs) > 0 {
			entry += "[" + strings.Join(scope.ResourceIDs, ",") + "]"
		}
		scopes = append(scopes, entry)
	}
	return map[string]any{
		"delegation_id": d.ID,
		"grantor_id":    d.PrincipalID,
		"module":        d.ModulePrefix,
		"scopes":        scopes,
	}
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

// RevokeModuleDelegation ends a delegation in orgID and returns it as it now
// stands; revoking one already revoked changes nothing. Its grantor may, and so
// may an administrator of the organisation (asAdmin, which the caller has
// established): anyone else is told there is none (ErrModuleDelegationNotFound),
// as GetDelegation tells them.
//
// A narrowing, so it runs under the policy log like every other revocation:
// witnessed before the row is stamped, the stamp, its audit record and the
// receipt committed in one transaction. It ends this delegation alone: every
// capability issued under it is sealed to it (DelegatedOperationBindingID) and
// fails its next verification, and nothing else its grantor holds moves.
func (s *Service) RevokeModuleDelegation(ctx context.Context, orgID, actorID, id string, asAdmin bool) (*ModuleDelegation, error) {
	if orgID == "" || actorID == "" || id == "" {
		return nil, ErrModuleDelegationNotFound
	}
	grantor := actorID
	if asAdmin {
		grantor = ""
	}
	var out *ModuleDelegation
	err := s.WithPolicyLoggedNarrowing(ctx, revokeModuleDelegationPolicyLogEntry(actorID, orgID, id), func(ctx context.Context) error {
		found, revoked, err := s.store.RevokeModuleDelegation(ctx, orgID, grantor, id, actorID)
		if err != nil {
			return err
		}
		if found == nil {
			return ErrModuleDelegationNotFound
		}
		out = found
		if !revoked {
			return nil
		}
		payload := moduleDelegationPayload(found)
		payload["revoked_by"] = actorID
		return s.emitTx(ctx, actorID, "user", EventModuleDelegationRevoked, "module_delegation", found.ID, orgID, payload)
	})
	return out, err
}
