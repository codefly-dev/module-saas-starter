package business

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"github.com/google/uuid"
)

// ModuleInstallationAuthorityVersion identifies the installation contract being
// fingerprinted, not a software release, credential or live authorization token.
const ModuleInstallationAuthorityVersion = "accounts.module-installation-authority/v1"

var ErrInstallationAuthorityVersion = errors.New("unsupported installation authority reference version")

// ModuleInstallationAuthorityReference is returned only on explicit request and
// after the existing policy and transactional installation checks pass. Consumers
// must retain the trusted Accounts instance alongside this opaque reference.
// Possession of the reference grants no authority and proves no current liveness.
type ModuleInstallationAuthorityReference struct {
	SchemaVersion string `json:"schemaVersion"`
	Digest        string `json:"digest"`
}

func moduleInstallationAuthority(caller ModuleCaller, delegation InstallerDelegation, req ModuleInstallationRequest, result *ModuleInstallationResult) (*ModuleInstallationAuthorityReference, error) {
	invalid := errors.New("invalid verified installation authority")
	if result == nil || result.State != "ready" || result.OrganizationID != delegation.OrganizationID || caller.BoundOrg != result.OrganizationID {
		return nil, invalid
	}
	// Only Accounts chooses these durable identities. Display labels, installer
	// credential/expiry, software/image versions and deployment receipts are absent.
	payload := map[string]any{
		"schema_version":      ModuleInstallationAuthorityVersion,
		"module_id":           req.ModuleID,
		"agent_identifier":    req.AgentIdentifier,
		"solution_identifier": req.SolutionIdentifier,
	}
	for key, value := range map[string]string{
		"organization_id":        result.OrganizationID,
		"principal_id":           result.PrincipalID,
		"installation_id":        result.InstallationID,
		"scope_node_id":          result.ScopeNodeID,
		"grant_id":               result.GrantID,
		"owner_principal_id":     delegation.OwnerPrincipalID,
		"installer_principal_id": caller.PrincipalID,
		"role_id":                delegation.RoleID,
	} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil {
			return nil, invalid
		}
		payload[key] = id.String()
	}
	for key, values := range map[string][]string{
		"role_permissions":  delegation.RolePermissions,
		"allowed_audiences": delegation.AllowedAudiences,
		"allowed_scopes":    delegation.AllowedScopes,
	} {
		if !boundedSet(values) {
			return nil, invalid
		}
		ordered := slices.Clone(values)
		slices.Sort(ordered)
		payload[key] = ordered
	}
	// encoding/json sorts object keys. Arrays above are sets with sorted values.
	// Disable HTML escaping; remove exactly the encoder's trailing newline. The
	// versioned contract retains JSON's Go escaping of U+2028/U+2029 and controls.
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return nil, invalid
	}
	sum := sha256.Sum256(bytes.TrimSuffix(canonical.Bytes(), []byte{'\n'}))
	return &ModuleInstallationAuthorityReference{
		SchemaVersion: ModuleInstallationAuthorityVersion,
		Digest:        "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}
