// Package rolecatalog parses an external permission catalog — a versioned JSON
// document of built-in role definitions maintained outside this repo — and
// diffs it against the roles already in the database. It is the domain core of
// the catalog importer: pure parsing, validation, and planning with no database
// dependency. The infra layer snapshots current state and applies a Plan; the
// CLI wires a file and a connection to it.
//
// This is distinct from the generated authorization (method-policy) catalog in
// module/AUTHORIZATION_CATALOG.md — that projects RPC policy; this one seeds
// RBAC roles.
package rolecatalog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SupportedVersion is the only catalog document version this importer accepts.
// A new incompatible shape bumps this constant rather than branching on it.
const SupportedVersion = 1

// namePattern constrains role names and scopes to a portable, log-safe subset
// that still admits the "scope:resource:action"-style identifiers products use
// (e.g. "module-a:analyst"). Deliberately excludes whitespace and control bytes.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_./-]*$`)

// tokenPattern constrains permission resource/action tokens. "*" is permitted
// for wildcard permissions, matching the seed admin role's "*:*".
var tokenPattern = regexp.MustCompile(`^(\*|[A-Za-z0-9][A-Za-z0-9:_./-]*)$`)

// Catalog is the parsed, canonicalized catalog document. After Parse, Roles is
// sorted by name and each role's Permissions are sorted, so an equivalent
// document always yields byte-identical planning regardless of source ordering.
type Catalog struct {
	Version uint32 `json:"version"`
	Roles   []Role `json:"roles"`
}

// Role is one built-in role definition.
type Role struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Scope       string       `json:"scope"`
	Permissions []Permission `json:"permissions"`
}

// Permission is a single resource:action grant.
type Permission struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

func (p Permission) String() string { return p.Resource + ":" + p.Action }

func less(a, b Permission) bool {
	if a.Resource != b.Resource {
		return a.Resource < b.Resource
	}
	return a.Action < b.Action
}

// Parse decodes and validates a catalog document, then canonicalizes it. Unknown
// fields are rejected so a typo in the source can't silently drop a role or
// permission. The returned Catalog is safe to Diff and to render deterministically.
func Parse(document []byte) (*Catalog, error) {
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	decoder.DisallowUnknownFields()

	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode role catalog: %w", err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("decode role catalog: trailing content after document")
	}
	if catalog.Version != SupportedVersion {
		return nil, fmt.Errorf("unsupported role catalog version %d (want %d)", catalog.Version, SupportedVersion)
	}

	seen := make(map[string]struct{}, len(catalog.Roles))
	for i := range catalog.Roles {
		role := &catalog.Roles[i]
		if !namePattern.MatchString(role.Name) {
			return nil, fmt.Errorf("role %q: name is empty or has unsupported characters", role.Name)
		}
		if _, dup := seen[role.Name]; dup {
			return nil, fmt.Errorf("role %q: duplicate role name", role.Name)
		}
		seen[role.Name] = struct{}{}
		if role.Scope != "" && !namePattern.MatchString(role.Scope) {
			return nil, fmt.Errorf("role %q: scope %q has unsupported characters", role.Name, role.Scope)
		}
		if err := canonicalizePermissions(role); err != nil {
			return nil, err
		}
	}

	sort.Slice(catalog.Roles, func(i, j int) bool { return catalog.Roles[i].Name < catalog.Roles[j].Name })
	return &catalog, nil
}

func canonicalizePermissions(role *Role) error {
	perms := make(map[Permission]struct{}, len(role.Permissions))
	for _, perm := range role.Permissions {
		if !tokenPattern.MatchString(perm.Resource) {
			return fmt.Errorf("role %q: permission resource %q is empty or invalid", role.Name, perm.Resource)
		}
		if !tokenPattern.MatchString(perm.Action) {
			return fmt.Errorf("role %q: permission action %q is empty or invalid", role.Name, perm.Action)
		}
		if _, dup := perms[perm]; dup {
			return fmt.Errorf("role %q: duplicate permission %s", role.Name, perm)
		}
		perms[perm] = struct{}{}
	}
	sort.Slice(role.Permissions, func(i, j int) bool { return less(role.Permissions[i], role.Permissions[j]) })
	return nil
}
