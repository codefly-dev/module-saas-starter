package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
)

// ModuleInstallationRequest describes one immutable organization installation.
// The accountable owner and delegation are never accepted from this request.
type ModuleInstallationRequest struct {
	ModuleID                string   `json:"moduleId"`
	OrganizationSlug        string   `json:"organizationSlug"`
	AgentIdentifier         string   `json:"agentIdentifier"`
	SolutionIdentifier      string   `json:"solutionIdentifier"`
	RoleID                  string   `json:"roleId"`
	ExpectedRolePermissions []string `json:"expectedRolePermissions"`
	AllowedAudiences        []string `json:"allowedAudiences"`
	AllowedScopes           []string `json:"allowedScopes"`
	DisplayName             string   `json:"displayName"`
	RootScopeLabel          string   `json:"rootScopeLabel"`
}

type ModuleInstallationResult struct {
	State          string `json:"state"`
	Changed        bool   `json:"changed"`
	OrganizationID string `json:"organizationId"`
	PrincipalID    string `json:"principalId,omitempty"`
	InstallationID string `json:"installationId,omitempty"`
	ScopeNodeID    string `json:"scopeNodeId,omitempty"`
	GrantID        string `json:"grantId,omitempty"`
}

// InstallerDelegation is an explicit bootstrap authorization by the organization
// owner. Deployment possession alone grants nothing. The policy projection is
// read on EVERY request; deleting a delegation revokes outstanding capabilities.
type InstallerDelegation struct {
	Prefix             string    `json:"prefix"`
	OrganizationID     string    `json:"organizationId"`
	ModuleID           string    `json:"moduleId"`
	AgentIdentifiers   []string  `json:"agentIdentifiers"`
	SolutionIdentifier string    `json:"solutionIdentifier"`
	RoleID             string    `json:"roleId"`
	RolePermissions    []string  `json:"rolePermissions"`
	AllowedAudiences   []string  `json:"allowedAudiences"`
	AllowedScopes      []string  `json:"allowedScopes"`
	OwnerPrincipalID   string    `json:"ownerPrincipalId"`
	ExpiresAt          time.Time `json:"expiresAt"`
}

type InstallerPolicy struct {
	Version     string                `json:"version"`
	Delegations []InstallerDelegation `json:"delegations"`
}

var ErrInstallerDenied = errors.New("installer delegation denied")

func exactSet(a, b []string) bool {
	aa, bb := slices.Clone(a), slices.Clone(b)
	slices.Sort(aa)
	slices.Sort(bb)
	return slices.Equal(aa, bb)
}
func boundedSet(values []string) bool {
	if len(values) == 0 || len(values) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if v == "" || strings.ContainsAny(v, "*\x00\n\r") || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

func ParseInstallerPolicy(reader io.Reader) (*InstallerPolicy, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	decoder.DisallowUnknownFields()
	var policy InstallerPolicy
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("invalid installer policy: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("installer policy has trailing data")
	}
	if policy.Version != "accounts.module-installation-policy/v1" {
		return nil, errors.New("unsupported installer policy version")
	}
	seen := map[string]bool{}
	for _, d := range policy.Delegations {
		key := d.Prefix + "/" + d.OrganizationID + "/" + d.ModuleID
		if seen[key] {
			return nil, errors.New("duplicate installer delegation")
		}
		seen[key] = true
		if !registrationIdentityPattern.MatchString(d.Prefix) || len(d.Prefix) > 63 || !looksLikeAgentIdentifier(d.ModuleID+":1") || d.SolutionIdentifier == "" || len(d.SolutionIdentifier) > 200 || d.ExpiresAt.IsZero() {
			return nil, errors.New("invalid installer delegation identity or expiry")
		}
		for _, id := range []string{d.OrganizationID, d.OwnerPrincipalID, d.RoleID} {
			if _, err := uuid.Parse(id); err != nil {
				return nil, errors.New("installer delegation requires server-issued UUID references")
			}
		}
		for _, set := range [][]string{d.AgentIdentifiers, d.RolePermissions, d.AllowedAudiences, d.AllowedScopes} {
			if !boundedSet(set) {
				return nil, errors.New("installer delegation requires nonempty, unique, bounded permissions and identifiers")
			}
		}
		for _, agent := range d.AgentIdentifiers {
			if !looksLikeAgentIdentifier(agent) || !strings.HasPrefix(agent, d.ModuleID+":") {
				return nil, errors.New("installer agent identifier is outside module")
			}
		}
	}
	return &policy, nil
}

func (p *InstallerPolicy) AllowsIdentity(caller ModuleCaller, now time.Time) bool {
	if p == nil {
		return false
	}
	for _, d := range p.Delegations {
		if ModulePrincipalID(d.Prefix) == caller.PrincipalID && d.OrganizationID == caller.BoundOrg && now.Before(d.ExpiresAt) {
			return true
		}
	}
	return false
}
func (p *InstallerPolicy) Authorize(caller ModuleCaller, req ModuleInstallationRequest, now time.Time) (InstallerDelegation, error) {
	if p != nil {
		for _, d := range p.Delegations {
			if ModulePrincipalID(d.Prefix) == caller.PrincipalID && d.OrganizationID == caller.BoundOrg && d.ModuleID == req.ModuleID && now.Before(d.ExpiresAt) && slices.Contains(d.AgentIdentifiers, req.AgentIdentifier) && d.SolutionIdentifier == req.SolutionIdentifier && d.RoleID == req.RoleID && exactSet(d.RolePermissions, req.ExpectedRolePermissions) && exactSet(d.AllowedAudiences, req.AllowedAudiences) && exactSet(d.AllowedScopes, req.AllowedScopes) {
				return d, nil
			}
		}
	}
	return InstallerDelegation{}, ErrInstallerDenied
}

type ModuleInstallationStore interface {
	ReconcileModuleInstallation(context.Context, *InstallSolutionParams, []string, bool) (*ModuleInstallationResult, error)
}

func (s *Service) ReconcileModuleInstallation(ctx context.Context, caller ModuleCaller, policy *InstallerPolicy, req ModuleInstallationRequest, apply bool) (*ModuleInstallationResult, error) {
	delegation, err := policy.Authorize(caller, req, time.Now())
	if err != nil {
		return nil, err
	}
	var org *gen.Organization
	err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		org, err = s.store.GetOrganizationBySlug(ctx, req.OrganizationSlug)
		return err
	})
	if err != nil {
		return nil, err
	}
	if org == nil || org.GetId() != delegation.OrganizationID {
		return nil, ErrInstallerDenied
	}
	store, ok := s.store.(ModuleInstallationStore)
	if !ok {
		return nil, errors.New("installer persistence unavailable")
	}
	var result *ModuleInstallationResult
	err = s.store.WithOrgTx(ctx, org.Id, func(ctx context.Context) error {
		var err error
		result, err = store.ReconcileModuleInstallation(ctx, &InstallSolutionParams{
			OrgID: org.Id, AgentIdentifier: req.AgentIdentifier, SolutionIdentifier: req.SolutionIdentifier,
			RoleID: req.RoleID, AllowedAudiences: req.AllowedAudiences, AllowedScopes: req.AllowedScopes,
			DisplayName: req.DisplayName, RootScopeLabel: req.RootScopeLabel,
			OwnerPrincipalID: delegation.OwnerPrincipalID, GrantedBy: delegation.OwnerPrincipalID,
			InstallerPrincipalID: caller.PrincipalID,
		}, delegation.RolePermissions, apply)
		if err != nil {
			return err
		}
		if !result.Changed {
			return nil
		}
		if err = s.publishLifecycleEvent(ctx, EventInstallationCreated, org.Id, result.ScopeNodeID, caller.PrincipalID, map[string]any{"installation_id": result.InstallationID, "agent_principal_id": result.PrincipalID, "solution_identifier": req.SolutionIdentifier}); err != nil {
			return err
		}
		return s.emitTx(ctx, caller.PrincipalID, ActorTypeSystem, EventInstallationCreated, "installation", result.InstallationID, org.Id, map[string]any{"agent_principal_id": result.PrincipalID, "solution_identifier": req.SolutionIdentifier, "role_id": req.RoleID, "owner_principal_id": delegation.OwnerPrincipalID})
	})
	return result, err
}
