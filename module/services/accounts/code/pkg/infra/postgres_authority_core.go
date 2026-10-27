package infra

// The authority core's store over accounts' Postgres.
//
// codefly-dev/authority's authority-core decides every operation of the host
// interfaces; this file is where accounts keeps what it decides. The core's
// own records (composed modules and their tombstones, installations, role
// grants, delegations, grant epochs, owner-aware revisions and consumed
// single-use nonces) live in the authority_core_* relations of migration 56,
// control-plane only: the core is the platform's authority and decides across
// organizations (a module delivered is installed at every one). Identity is
// accounts' own and is only read here: organizations, memberships, teams, and
// the organization roles a member holds, synthesized from accounts' RBAC
// (role assignments, scope grants and the members' contributed permissions).
// The organization's authorization revision is accounts' own lever, so every
// RBAC change accounts records already revokes what the core issued.
//
// Read runs at REPEATABLE READ, READ ONLY; Write at SERIALIZABLE, retried on a
// serialization failure and reported as authority.ErrConflict when it keeps
// conflicting. Both run under the control-plane role.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/scope"
	resources "github.com/codefly-dev/authority/libraries/resources/go"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"accounts/pkg/business"
)

// authorityCoreAttempts bounds how often a conflicting write is retried.
const authorityCoreAttempts = 8

// AuthorityCoreStore is the core's Store over this host's Postgres.
type AuthorityCoreStore struct {
	s       *PostgresStore
	consent business.ModuleConsent
}

var _ authority.Store = AuthorityCoreStore{}

// AuthorityCore is the core's store over this database, reading a module
// installation's consent as allowing every binding as delivered: for the
// store contract's tests. A host serves through AuthorityCoreWithConsent.
func (s *PostgresStore) AuthorityCore() authority.Store { return AuthorityCoreStore{s: s} }

// AuthorityCoreWithConsent is the core's store over this database, answering
// what each module installation's consent allows (business.ModuleConsent).
func (s *PostgresStore) AuthorityCoreWithConsent(consent business.ModuleConsent) authority.Store {
	return AuthorityCoreStore{s: s, consent: consent}
}

// Read runs fn over one consistent snapshot.
func (c AuthorityCoreStore) Read(ctx context.Context, fn func(authority.Tx) error) error {
	recordControlPlane(ctx)
	return c.s.withControlPlaneTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context) error {
		return fn(coreTx{ctx: ctx, q: c.s.getQueryExecutor(ctx), consent: c.consent})
	})
}

// Write runs fn as one serializable transaction, retrying it on a
// serialization failure; fn is free of other effects, as the Store contract
// requires.
func (c AuthorityCoreStore) Write(ctx context.Context, fn func(authority.Tx) error) error {
	recordControlPlane(ctx)
	var err error
	for attempt := range authorityCoreAttempts {
		err = c.s.withControlPlaneTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(ctx context.Context) error {
			return fn(coreTx{ctx: ctx, q: c.s.getQueryExecutor(ctx), write: true, consent: c.consent})
		})
		if !serializationFailure(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return fmt.Errorf("%w: %w", authority.ErrConflict, err)
}

func serializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

type coreTx struct {
	ctx     context.Context
	q       QueryExecutor
	write   bool
	consent business.ModuleConsent
}

// Context is the context this transaction's writes bind to: what the host's
// journal records its audit in.
func (t coreTx) Context() context.Context { return t.ctx }

func coreMissing(what, id string) error { return fmt.Errorf("%s %q: %w", what, id, domain.ErrNotFound) }

func (t coreTx) mustWrite() error {
	if !t.write {
		return errors.New("authority core: a write inside Read")
	}
	return nil
}

// asUUID is id when it is a UUID; identity ids are UUIDs here, so anything
// else names nothing.
func asUUID(id string) (uuid.UUID, bool) {
	parsed, err := uuid.Parse(id)
	return parsed, err == nil
}

// --- identity, read only ----------------------------------------------------

func (t coreTx) Organizations() ([]domain.Organization, error) {
	rows, err := t.q.Query(t.ctx, `SELECT id::text, name FROM public.organizations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Organization, error) {
		var o domain.Organization
		return o, row.Scan(&o.ID, &o.Name)
	})
}

func (t coreTx) Organization(id string) (domain.Organization, error) {
	org, ok := asUUID(id)
	if !ok {
		return domain.Organization{}, coreMissing("organization", id)
	}
	o := domain.Organization{ID: id}
	err := t.q.QueryRow(t.ctx, `SELECT name FROM public.organizations WHERE id = $1`, org).Scan(&o.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Organization{}, coreMissing("organization", id)
	}
	return o, err
}

// Person answers whether id names a person of this host at all.
func (t coreTx) Person(id string) (domain.Person, error) {
	user, ok := asUUID(id)
	if !ok {
		return domain.Person{}, coreMissing("person", id)
	}
	var exists bool
	if err := t.q.QueryRow(t.ctx, `SELECT EXISTS (SELECT 1 FROM public.users WHERE uuid = $1)`, user).Scan(&exists); err != nil {
		return domain.Person{}, err
	}
	if !exists {
		return domain.Person{}, coreMissing("person", id)
	}
	return domain.Person{ID: id}, nil
}

// Member is a person as a member of one organization: their membership role,
// their teams there, and the organization roles they hold (memberRoles).
func (t coreTx) Member(organization, id string) (domain.Person, error) {
	if _, ok := asUUID(id); !ok {
		// An empty or malformed id names nobody; it must never read as "every
		// member" below.
		return domain.Person{}, coreMissing("member of "+organization, id)
	}
	people, err := t.members(organization, id)
	if err != nil {
		return domain.Person{}, err
	}
	if len(people) == 0 {
		return domain.Person{}, coreMissing("member of "+organization, id)
	}
	return people[0], nil
}

func (t coreTx) People(organization string) ([]domain.Person, error) {
	return t.members(organization, "")
}

// members reads an organization's members, or the one person named.
func (t coreTx) members(organization, person string) ([]domain.Person, error) {
	org, ok := asUUID(organization)
	if !ok {
		return nil, nil
	}
	var only *uuid.UUID
	if person != "" {
		user, ok := asUUID(person)
		if !ok {
			return nil, nil
		}
		only = &user
	}
	rows, err := t.q.Query(t.ctx, `
		SELECT m.user_id::text, m.role,
		       COALESCE((SELECT array_agg(tm.team_id::text ORDER BY tm.team_id)
		                   FROM public.team_members tm JOIN public.teams tt ON tt.id = tm.team_id
		                  WHERE tm.user_id = m.user_id AND tt.org_id = m.org_id), ARRAY[]::text[]),
		       COALESCE((SELECT array_agg(a.id::text ORDER BY a.id)
		                   FROM public.role_assignments a
		                  WHERE a.org_id = m.org_id
		                    AND ((a.subject_kind = 'principal' AND a.subject_id = m.user_id)
		                      OR (a.subject_kind = 'team' AND a.subject_id IN (
		                             SELECT tm.team_id FROM public.team_members tm JOIN public.teams tt ON tt.id = tm.team_id
		                              WHERE tm.user_id = m.user_id AND tt.org_id = m.org_id)))), ARRAY[]::text[]),
		       COALESCE((SELECT array_agg(g.id::text ORDER BY g.id)
		                   FROM public.scope_grants g
		                  WHERE g.org_id = m.org_id AND (g.expires_at IS NULL OR g.expires_at > now())
		                    AND ((g.subject_kind = 'principal' AND g.subject_id = m.user_id)
		                      OR (g.subject_kind = 'team' AND g.subject_id IN (
		                             SELECT tm.team_id FROM public.team_members tm JOIN public.teams tt ON tt.id = tm.team_id
		                              WHERE tm.user_id = m.user_id AND tt.org_id = m.org_id)))), ARRAY[]::text[])
		  FROM public.organization_members m
		 WHERE m.org_id = $1 AND ($2::uuid IS NULL OR m.user_id = $2)
		 ORDER BY m.user_id`, org, only)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Person, error) {
		p := domain.Person{Organization: organization}
		var membership string
		var assignments, grants []string
		if err := row.Scan(&p.ID, &membership, &p.Teams, &assignments, &grants); err != nil {
			return p, err
		}
		p.Roles = memberRoles(membership, assignments, grants)
		return p, nil
	})
}

// The organization roles a member holds, as the core names them: the
// membership's administrator role ("owner" or "admin", every action on every
// kind the audience serves), the members' contributed permissions, and one
// role per role assignment and per live scope grant reaching them, directly
// or through a team.
const (
	membersRole          = "members"
	assignmentRolePrefix = "assignment:"
	scopeGrantRolePrefix = "scope-grant:"
)

func memberRoles(membership string, assignments, grants []string) []string {
	var roles []string
	if membership == "owner" || membership == "admin" {
		roles = append(roles, membership)
	}
	if len(business.MemberPermissions()) > 0 {
		roles = append(roles, membersRole)
	}
	for _, id := range assignments {
		roles = append(roles, assignmentRolePrefix+id)
	}
	for _, id := range grants {
		roles = append(roles, scopeGrantRolePrefix+id)
	}
	return roles
}

// OrgRole answers one of the roles memberRoles names.
func (t coreTx) OrgRole(organization, name string) (domain.OrgRole, error) {
	role := domain.OrgRole{Organization: organization, Name: name}
	org, ok := asUUID(organization)
	if !ok {
		return role, coreMissing("role", organization+"/"+name)
	}
	switch {
	case name == "owner" || name == "admin":
		role.Scopes = []domain.RoleScope{{Kind: scope.AnyKind, Actions: []string{scope.AnyAction}}}
		return role, nil
	case name == membersRole:
		for _, p := range business.MemberPermissions() {
			role.Scopes = append(role.Scopes, domain.RoleScope{Kind: p.Resource, Actions: []string{p.Action}})
		}
		return role, nil
	case strings.HasPrefix(name, assignmentRolePrefix):
		id, ok := asUUID(strings.TrimPrefix(name, assignmentRolePrefix))
		if !ok {
			break
		}
		return t.grantedRole(role, `
			SELECT p.resource, p.action, a.scope
			  FROM public.role_assignments a JOIN public.role_permissions p ON p.role_id = a.role_id
			 WHERE a.org_id = $1 AND a.id = $2
			 ORDER BY p.resource, p.action`, org, id)
	case strings.HasPrefix(name, scopeGrantRolePrefix):
		id, ok := asUUID(strings.TrimPrefix(name, scopeGrantRolePrefix))
		if !ok {
			break
		}
		return t.grantedRole(role, `
			SELECT p.resource, p.action, COALESCE(n.resource_id, n.id::text)
			  FROM public.scope_grants g
			  JOIN public.role_permissions p ON p.role_id = g.role_id
			  LEFT JOIN public.scope_nodes n ON n.org_id = g.org_id AND n.scope_path = g.scope_path
			 WHERE g.org_id = $1 AND g.id = $2 AND (g.expires_at IS NULL OR g.expires_at > now())
			 ORDER BY p.resource, p.action`, org, id)
	}
	return role, coreMissing("role", organization+"/"+name)
}

// grantedRole reads a role's permissions, each narrowed to the resource its
// grant names (none: the whole kind). "*" is every kind or every action.
func (t coreTx) grantedRole(role domain.OrgRole, query string, args ...any) (domain.OrgRole, error) {
	rows, err := t.q.Query(t.ctx, query, args...)
	if err != nil {
		return role, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
		var resource, action string
		var narrowed *string
		if err := rows.Scan(&resource, &action, &narrowed); err != nil {
			return role, err
		}
		sc := domain.RoleScope{Kind: resource, Actions: []string{action}}
		if narrowed != nil && *narrowed != "" {
			sc.Resources = []resources.Grant{{ID: *narrowed}}
		}
		role.Scopes = append(role.Scopes, sc)
	}
	if err := rows.Err(); err != nil {
		return role, err
	}
	if !found {
		return role, coreMissing("role", role.Organization+"/"+role.Name)
	}
	return role, nil
}

// --- modules and tombstones -------------------------------------------------

// moduleDocument is a module's declarations as stored.
type moduleDocument struct {
	ResourceKinds []storedScope            `json:"resource_kinds,omitempty"`
	Bindings      map[string]storedBinding `json:"bindings,omitempty"`
	Roles         map[string][]storedScope `json:"roles,omitempty"`
	OwnedRoles    map[string][]storedScope `json:"owned_roles,omitempty"`
	OwnerAware    bool                     `json:"owner_aware,omitempty"`
	Methods       map[string]storedMethod  `json:"methods,omitempty"`
	HasRoles      bool                     `json:"has_roles,omitempty"`
	HasMethods    bool                     `json:"has_methods,omitempty"`
}

type storedScope struct {
	Kind    string   `json:"kind"`
	Actions []string `json:"actions,omitempty"`
	IDs     []string `json:"ids,omitempty"`
	Owner   string   `json:"owner,omitempty"`
}

type storedBinding struct {
	Audience string        `json:"audience"`
	Invoke   []storedScope `json:"invoke,omitempty"`
	Headless []storedScope `json:"headless,omitempty"`
	Lookup   []storedScope `json:"lookup,omitempty"`
}

type storedMethod struct {
	Requires         []storedScope `json:"requires,omitempty"`
	AnyCheckedCaller bool          `json:"any_checked_caller,omitempty"`
}

func storeScopes(in []*basev0.WorkScopeV1) []storedScope {
	out := make([]storedScope, 0, len(in))
	for _, sc := range in {
		out = append(out, storedScope{Kind: sc.GetResourceKind(), Actions: slices.Clone(sc.GetActions()), IDs: slices.Clone(sc.GetResourceIds()), Owner: sc.GetResourceOwnerId()})
	}
	return out
}

func loadScopes(in []storedScope) []*basev0.WorkScopeV1 {
	if in == nil {
		return nil
	}
	out := make([]*basev0.WorkScopeV1, 0, len(in))
	for _, sc := range in {
		out = append(out, &basev0.WorkScopeV1{ResourceKind: sc.Kind, Actions: slices.Clone(sc.Actions), ResourceIds: slices.Clone(sc.IDs), ResourceOwnerId: sc.Owner})
	}
	return out
}

func encodeModule(m domain.Module) ([]byte, error) {
	doc := moduleDocument{ResourceKinds: storeScopes(m.ResourceKinds), OwnerAware: m.OwnerAware, HasRoles: m.Roles != nil, HasMethods: m.Methods != nil}
	if len(m.Bindings) > 0 {
		doc.Bindings = map[string]storedBinding{}
		for id, b := range m.Bindings {
			doc.Bindings[id] = storedBinding{Audience: b.Audience, Invoke: storeScopes(b.InvokeScopes), Headless: storeScopes(b.HeadlessScopes), Lookup: storeScopes(b.LookupScopes)}
		}
	}
	if len(m.Roles) > 0 {
		doc.Roles = map[string][]storedScope{}
		for name, scopes := range m.Roles {
			doc.Roles[name] = storeScopes(scopes)
		}
	}
	if len(m.OwnedRoles) > 0 {
		doc.OwnedRoles = map[string][]storedScope{}
		for name, scopes := range m.OwnedRoles {
			doc.OwnedRoles[name] = storeScopes(scopes)
		}
	}
	if len(m.Methods) > 0 {
		doc.Methods = map[string]storedMethod{}
		for method, p := range m.Methods {
			doc.Methods[method] = storedMethod{Requires: storeScopes(p.Requires), AnyCheckedCaller: p.AnyCheckedCaller}
		}
	}
	return json.Marshal(doc)
}

func decodeModule(m *domain.Module, raw []byte) error {
	var doc moduleDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("module %s: stored declarations do not read: %w", m.Name, err)
	}
	m.ResourceKinds, m.OwnerAware = loadScopes(doc.ResourceKinds), doc.OwnerAware
	m.Bindings = map[string]domain.Binding{}
	for id, b := range doc.Bindings {
		m.Bindings[id] = domain.Binding{ID: id, Audience: b.Audience, InvokeScopes: loadScopes(b.Invoke), HeadlessScopes: loadScopes(b.Headless), LookupScopes: loadScopes(b.Lookup)}
	}
	if doc.HasRoles || len(doc.Roles) > 0 {
		m.Roles = map[string][]*basev0.WorkScopeV1{}
		for name, scopes := range doc.Roles {
			m.Roles[name] = loadScopes(scopes)
		}
	}
	m.OwnedRoles = map[string][]*basev0.WorkScopeV1{}
	for name, scopes := range doc.OwnedRoles {
		m.OwnedRoles[name] = loadScopes(scopes)
	}
	if doc.HasMethods || len(doc.Methods) > 0 {
		m.Methods = map[string]domain.MethodPolicy{}
		for method, p := range doc.Methods {
			m.Methods[method] = domain.MethodPolicy{Requires: loadScopes(p.Requires), AnyCheckedCaller: p.AnyCheckedCaller}
		}
	}
	return nil
}

const coreModuleColumns = `name, principal, kind, generation, stand_in, declarations`

func scanCoreModule(row pgx.Row) (domain.Module, error) {
	var m domain.Module
	var generation int64
	var raw []byte
	if err := row.Scan(&m.Name, &m.Principal, &m.Kind, &generation, &m.StandIn, &raw); err != nil {
		return m, err
	}
	m.Generation = uint64(generation)
	return m, decodeModule(&m, raw)
}

func (t coreTx) moduleWhere(clause, what, id string) (domain.Module, error) {
	m, err := scanCoreModule(t.q.QueryRow(t.ctx, `SELECT `+coreModuleColumns+` FROM public.authority_core_modules WHERE `+clause, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Module{}, coreMissing(what, id)
	}
	return m, err
}

func (t coreTx) Module(principal string) (domain.Module, error) {
	return t.moduleWhere(`principal = $1`, "audience", principal)
}

func (t coreTx) ModuleNamed(name string) (domain.Module, error) {
	return t.moduleWhere(`name = $1`, "module", name)
}

func (t coreTx) Modules() ([]domain.Module, error) {
	rows, err := t.q.Query(t.ctx, `SELECT `+coreModuleColumns+` FROM public.authority_core_modules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Module, error) { return scanCoreModule(row) })
}

func (t coreTx) PutModule(m domain.Module) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	raw, err := encodeModule(m)
	if err != nil {
		return err
	}
	_, err = t.q.Exec(t.ctx, `
		INSERT INTO public.authority_core_modules (name, principal, kind, generation, stand_in, declarations)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (name) DO UPDATE SET principal = EXCLUDED.principal, kind = EXCLUDED.kind, generation = EXCLUDED.generation,
		       stand_in = EXCLUDED.stand_in, declarations = EXCLUDED.declarations`,
		m.Name, m.Principal, m.Kind, int64(m.Generation), m.StandIn, raw)
	return err
}

func (t coreTx) DeleteModule(name string) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	_, err := t.q.Exec(t.ctx, `DELETE FROM public.authority_core_modules WHERE name = $1`, name)
	return err
}

func (t coreTx) tombstoneWhere(clause, what, id string) (domain.Tombstone, error) {
	var tomb domain.Tombstone
	var generation int64
	err := t.q.QueryRow(t.ctx, `SELECT name, principal, generation FROM public.authority_core_tombstones WHERE `+clause, id).Scan(&tomb.Name, &tomb.Principal, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Tombstone{}, coreMissing(what, id)
	}
	tomb.Generation = uint64(generation)
	return tomb, err
}

func (t coreTx) Tombstone(name string) (domain.Tombstone, error) {
	return t.tombstoneWhere(`name = $1`, "tombstone", name)
}

func (t coreTx) TombstoneByPrincipal(principal string) (domain.Tombstone, error) {
	return t.tombstoneWhere(`principal = $1`, "tombstone of principal", principal)
}

func (t coreTx) PutTombstone(tomb domain.Tombstone) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	_, err := t.q.Exec(t.ctx, `
		INSERT INTO public.authority_core_tombstones (name, principal, generation) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET principal = EXCLUDED.principal, generation = EXCLUDED.generation`,
		tomb.Name, tomb.Principal, int64(tomb.Generation))
	return err
}

// --- installations and role grants ------------------------------------------

const coreInstallationColumns = `id, organization_id::text, target, status, revision, agent_role, principal_id, installed_by, consent, owned`

func scanCoreInstallation(row pgx.Row) (domain.Installation, error) {
	var inst domain.Installation
	var status string
	var revision int64
	var consent, owned []byte
	if err := row.Scan(&inst.ID, &inst.Organization, &inst.Target, &status, &revision, &inst.AgentRole, &inst.PrincipalID, &inst.InstalledBy, &consent, &owned); err != nil {
		return inst, err
	}
	inst.Status, inst.Revision = domain.Status(status), uint64(revision)
	if err := json.Unmarshal(consent, &inst.Consent); err != nil {
		return inst, fmt.Errorf("installation %s: stored consent does not read: %w", inst.ID, err)
	}
	inst.Owned = map[string][]string{}
	if err := json.Unmarshal(owned, &inst.Owned); err != nil {
		return inst, fmt.Errorf("installation %s: stored attachments do not read: %w", inst.ID, err)
	}
	return inst, nil
}

// A module's installation is accounts' own record (public.installations,
// kind 'module'): an administrator's consent within the module envelope,
// retired by an uninstall or by a ceiling that no longer allows the
// organization, all in accounts' own transactions. The core is configured with
// HostInstallsModules, so it reads those records as its installations, live,
// and writes a module's only to raise its revision when a redelivery narrows
// the module; the raise is kept beside the record
// (authority_core_module_revisions). authority_core_installations holds the
// solutions' installations, which the core decides.

// hostModules maps the principal accounts records a module's installation
// under (business.ModulePrincipalID of its name) to every composed module.
func (t coreTx) hostModules() (map[uuid.UUID]string, error) {
	rows, err := t.q.Query(t.ctx, `SELECT name FROM public.authority_core_modules WHERE kind <> 'solution'`)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(names))
	for _, name := range names {
		principal, err := uuid.Parse(business.ModulePrincipalID(name))
		if err != nil {
			return nil, err
		}
		out[principal] = name
	}
	return out, nil
}

// moduleInstallations are accounts' module installation records of composed
// modules matching where (with its arguments from $2), as the core reads them.
func (t coreTx) moduleInstallations(where string, args ...any) ([]domain.Installation, error) {
	modules, err := t.hostModules()
	if err != nil || len(modules) == 0 {
		return nil, err
	}
	principals := make([]uuid.UUID, 0, len(modules))
	for principal := range modules {
		principals = append(principals, principal)
	}
	// The committed envelope revision, read in this transaction, so the
	// consent is judged against the ceiling in force and never a registry a
	// newer envelope has replaced.
	var envelope uint64
	if t.consent != nil {
		var committed int64
		if err := t.q.QueryRow(t.ctx, `SELECT COALESCE(max(revision), 0) FROM public.module_envelopes`).Scan(&committed); err != nil {
			return nil, err
		}
		envelope = uint64(committed)
	}
	rows, err := t.q.Query(t.ctx, `
		SELECT i.id, i.org_id, i.registry_principal_id, i.status, i.revision + COALESCE(r.raised, 0), i.scope
		  FROM public.installations i
		  LEFT JOIN public.authority_core_module_revisions r ON r.installation_id = i.id
		 WHERE i.kind = 'module' AND i.registry_principal_id = ANY($1)`+where+`
		 ORDER BY i.id`, append([]any{principals}, args...)...)
	if err != nil {
		return nil, err
	}
	type record struct {
		inst  domain.Installation
		scope []byte
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (record, error) {
		var id, org, principal uuid.UUID
		var status string
		var revision int64
		var consented []byte
		if err := row.Scan(&id, &org, &principal, &status, &revision, &consented); err != nil {
			return record{}, err
		}
		return record{inst: domain.Installation{ID: id.String(), Organization: org.String(), Target: modules[principal],
			Status: moduleInstallationStatus(status), Revision: uint64(revision), Owned: map[string][]string{}}, scope: consented}, nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Installation, 0, len(records))
	for _, r := range records {
		// What the administrator consented to, within the module envelope:
		// an installation whose consent allows no capability is not one the
		// core may issue through, and the bindings it allows bound every
		// issue through them (domain.Installation.Bindings).
		if t.consent != nil {
			bindings, active, err := t.consent(r.inst.Target, r.scope, envelope)
			if err != nil {
				return nil, err
			}
			if !active && r.inst.Status == domain.Active {
				r.inst.Status = domain.Pending
			}
			r.inst.Bindings = bindings
		}
		out = append(out, r.inst)
	}
	return out, nil
}

// moduleInstallationStatus is how the core reads accounts' lifecycle: only an
// active installation is one; consent awaiting its install step, or one that
// failed it, is pending.
func moduleInstallationStatus(status string) domain.Status {
	switch status {
	case "active":
		return domain.Active
	case "revoked":
		return domain.Revoked
	default:
		return domain.Pending
	}
}

func (t coreTx) Installation(id string) (domain.Installation, error) {
	inst, err := scanCoreInstallation(t.q.QueryRow(t.ctx, `SELECT `+coreInstallationColumns+` FROM public.authority_core_installations WHERE id = $1`, id))
	if !errors.Is(err, pgx.ErrNoRows) {
		return inst, err
	}
	if native, ok := asUUID(id); ok {
		found, err := t.moduleInstallations(` AND i.id = $2`, native)
		if err != nil {
			return domain.Installation{}, err
		}
		if len(found) == 1 {
			return found[0], nil
		}
	}
	return domain.Installation{}, coreMissing("installation", id)
}

func (t coreTx) InstallationByPrincipal(principal string) (domain.Installation, error) {
	if principal == "" {
		return domain.Installation{}, coreMissing("principal", principal)
	}
	inst, err := scanCoreInstallation(t.q.QueryRow(t.ctx, `SELECT `+coreInstallationColumns+` FROM public.authority_core_installations WHERE principal_id = $1`, principal))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Installation{}, coreMissing("principal", principal)
	}
	return inst, err
}

// Installations are the solutions' installations the core decided and the
// module installations accounts holds now (a retired one is read by its id).
func (t coreTx) Installations(organization string) ([]domain.Installation, error) {
	// A module's row the core wrote before accounts held its modules' records
	// is not an installation any more.
	const solutions = `SELECT ` + coreInstallationColumns + ` FROM public.authority_core_installations
		WHERE target NOT IN (SELECT name FROM public.authority_core_modules WHERE kind <> 'solution')`
	var rows pgx.Rows
	var modules []domain.Installation
	var err error
	if organization == "" {
		if modules, err = t.moduleInstallations(` AND i.status <> 'revoked'`); err == nil {
			rows, err = t.q.Query(t.ctx, solutions+` ORDER BY id`)
		}
	} else {
		org, ok := asUUID(organization)
		if !ok {
			return nil, nil
		}
		if modules, err = t.moduleInstallations(` AND i.status <> 'revoked' AND i.org_id = $2`, org); err == nil {
			rows, err = t.q.Query(t.ctx, solutions+` AND organization_id = $1 ORDER BY id`, org)
		}
	}
	if err != nil {
		return nil, err
	}
	decided, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Installation, error) { return scanCoreInstallation(row) })
	if err != nil {
		return nil, err
	}
	all := append(decided, modules...)
	sort.Slice(all, func(a, b int) bool { return all[a].ID < all[b].ID })
	return all, nil
}

func (t coreTx) PutInstallation(inst domain.Installation) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	if handled, err := t.raiseModuleInstallation(inst); handled || err != nil {
		return err
	}
	org, ok := asUUID(inst.Organization)
	if !ok {
		return fmt.Errorf("installation %s names organization %q, which is not one of this host's", inst.ID, inst.Organization)
	}
	consent, err := json.Marshal(inst.Consent)
	if err != nil {
		return err
	}
	owned := map[string][]string{}
	for kind, ids := range inst.Owned {
		if len(ids) > 0 {
			owned[kind] = ids
		}
	}
	ownedRaw, err := json.Marshal(owned)
	if err != nil {
		return err
	}
	_, err = t.q.Exec(t.ctx, `
		INSERT INTO public.authority_core_installations (id, organization_id, target, status, revision, agent_role, principal_id, installed_by, consent, owned)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO UPDATE SET organization_id = EXCLUDED.organization_id, target = EXCLUDED.target, status = EXCLUDED.status,
		       revision = EXCLUDED.revision, agent_role = EXCLUDED.agent_role, principal_id = EXCLUDED.principal_id,
		       installed_by = EXCLUDED.installed_by, consent = EXCLUDED.consent, owned = EXCLUDED.owned`,
		inst.ID, org, inst.Target, string(inst.Status), int64(inst.Revision), inst.AgentRole, inst.PrincipalID, inst.InstalledBy, consent, ownedRaw)
	return err
}

// raiseModuleInstallation applies the one write the core makes to a module's
// installation: raising its revision, when a redelivery narrows the module.
// It reports whether inst is a module's installation at all; any other change
// to one is refused, because the record is accounts' own.
func (t coreTx) raiseModuleInstallation(inst domain.Installation) (bool, error) {
	var kind string
	err := t.q.QueryRow(t.ctx, `SELECT kind FROM public.authority_core_modules WHERE name = $1`, inst.Target).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind == domain.KindSolution) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	current, err := t.Installation(inst.ID)
	if err != nil {
		return true, fmt.Errorf("module installation %s is not one accounts holds: %w", inst.ID, err)
	}
	if current.Organization != inst.Organization || current.Target != inst.Target || current.Status != inst.Status ||
		inst.AgentRole != "" || inst.PrincipalID != "" || inst.InstalledBy != "" || len(inst.Owned) > 0 || inst.Revision <= current.Revision {
		return true, fmt.Errorf("module installation %s is accounts' own record: the authority core only raises its revision", inst.ID)
	}
	_, err = t.q.Exec(t.ctx, `
		INSERT INTO public.authority_core_module_revisions (installation_id, raised) VALUES ($1, $2)
		ON CONFLICT (installation_id) DO UPDATE SET raised = public.authority_core_module_revisions.raised + EXCLUDED.raised`,
		inst.ID, int64(inst.Revision-current.Revision))
	return true, err
}

func (t coreTx) RoleGrants(installationID string) ([]domain.RoleGrant, error) {
	rows, err := t.q.Query(t.ctx, `SELECT role, person_id, team_id FROM public.authority_core_role_grants WHERE installation_id = $1 ORDER BY role, person_id, team_id`, installationID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.RoleGrant, error) {
		g := domain.RoleGrant{InstallationID: installationID}
		return g, row.Scan(&g.Role, &g.PersonID, &g.TeamID)
	})
}

func (t coreTx) PutRoleGrant(g domain.RoleGrant) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	_, err := t.q.Exec(t.ctx, `
		INSERT INTO public.authority_core_role_grants (installation_id, role, person_id, team_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, g.InstallationID, g.Role, g.PersonID, g.TeamID)
	return err
}

func (t coreTx) DeleteRoleGrant(g domain.RoleGrant) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	_, err := t.q.Exec(t.ctx, `
		DELETE FROM public.authority_core_role_grants WHERE installation_id = $1 AND role = $2 AND person_id = $3 AND team_id = $4`,
		g.InstallationID, g.Role, g.PersonID, g.TeamID)
	return err
}

func (t coreTx) DeleteRoleGrants(installationID string) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	_, err := t.q.Exec(t.ctx, `DELETE FROM public.authority_core_role_grants WHERE installation_id = $1`, installationID)
	return err
}

// --- revisions and epochs ---------------------------------------------------

// AuthorizationRevision is accounts' own organization revision: every RBAC
// change accounts records moves it, and so does every bump the core makes.
func (t coreTx) AuthorizationRevision(organization string) (uint64, error) {
	org, ok := asUUID(organization)
	if !ok {
		return 0, coreMissing("organization", organization)
	}
	var revision *int64
	err := t.q.QueryRow(t.ctx, `
		SELECT r.revision FROM public.organizations o
		  LEFT JOIN public.organization_authorization_revisions r ON r.org_id = o.id
		 WHERE o.id = $1`, org).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, coreMissing("organization", organization)
	}
	if err != nil {
		return 0, err
	}
	if revision == nil || *revision <= 0 {
		return 0, fmt.Errorf("organization %s has no authorization revision", organization)
	}
	return uint64(*revision), nil
}

func (t coreTx) BumpAuthorizationRevision(organization string) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	org, ok := asUUID(organization)
	if !ok {
		return coreMissing("organization", organization)
	}
	_, err := t.q.Exec(t.ctx, `SELECT public.bump_organization_authorization_revision($1)`, org)
	return err
}

// PersonEpoch moves when the core is told a person changed, and whenever
// accounts moves the person's own authorization revision in any of their
// organizations: the sum only grows, because each term only grows.
func (t coreTx) PersonEpoch(person string) (uint64, error) {
	var epoch int64
	user, isUser := asUUID(person)
	var principal *uuid.UUID
	if isUser {
		principal = &user
	}
	err := t.q.QueryRow(t.ctx, `
		SELECT COALESCE((SELECT epoch FROM public.authority_core_person_epochs WHERE person_id = $1), 0)
		     + COALESCE((SELECT sum(revision) FROM public.principal_authorization_revisions WHERE $2::uuid IS NOT NULL AND principal_id = $2), 0)
		     + 1`, person, principal).Scan(&epoch)
	return uint64(epoch), err
}

func (t coreTx) BumpPersonEpoch(person string) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	_, err := t.q.Exec(t.ctx, `
		INSERT INTO public.authority_core_person_epochs (person_id, epoch) VALUES ($1, 1)
		ON CONFLICT (person_id) DO UPDATE SET epoch = public.authority_core_person_epochs.epoch + 1`, person)
	return err
}

func (t coreTx) GrantEpoch(person, installationID string) (uint64, error) {
	var epoch int64
	err := t.q.QueryRow(t.ctx, `SELECT COALESCE((SELECT epoch FROM public.authority_core_grant_epochs WHERE person_id = $1 AND installation_id = $2), 0)`,
		person, installationID).Scan(&epoch)
	return uint64(epoch), err
}

func (t coreTx) BumpGrantEpoch(person, installationID string) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	_, err := t.q.Exec(t.ctx, `
		INSERT INTO public.authority_core_grant_epochs (person_id, installation_id, epoch) VALUES ($1, $2, 1)
		ON CONFLICT (person_id, installation_id) DO UPDATE SET epoch = public.authority_core_grant_epochs.epoch + 1`, person, installationID)
	return err
}

func (t coreTx) OwnerAwareRevision(audience string) (uint64, error) {
	var revision int64
	err := t.q.QueryRow(t.ctx, `SELECT COALESCE((SELECT revision FROM public.authority_core_owner_aware WHERE audience = $1), 0)`, audience).Scan(&revision)
	return uint64(revision), err
}

func (t coreTx) BumpOwnerAwareRevision(audience string) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	_, err := t.q.Exec(t.ctx, `
		INSERT INTO public.authority_core_owner_aware (audience, revision) VALUES ($1, 1)
		ON CONFLICT (audience) DO UPDATE SET revision = public.authority_core_owner_aware.revision + 1`, audience)
	return err
}

// --- delegations ------------------------------------------------------------

const coreDelegationColumns = `id, organization_id::text, grantor, module, purpose, scopes, revoked`

func scanCoreDelegation(row pgx.Row) (domain.Delegation, error) {
	var d domain.Delegation
	var raw []byte
	if err := row.Scan(&d.ID, &d.Organization, &d.Grantor, &d.Module, &d.Purpose, &raw, &d.Revoked); err != nil {
		return d, err
	}
	var scopes []storedScope
	if err := json.Unmarshal(raw, &scopes); err != nil {
		return d, fmt.Errorf("delegation %s: stored scopes do not read: %w", d.ID, err)
	}
	d.Scopes = loadScopes(scopes)
	return d, nil
}

func (t coreTx) Delegation(id string) (domain.Delegation, error) {
	d, err := scanCoreDelegation(t.q.QueryRow(t.ctx, `SELECT `+coreDelegationColumns+` FROM public.authority_core_delegations WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Delegation{}, coreMissing("delegation", id)
	}
	return d, err
}

func (t coreTx) Delegations() ([]domain.Delegation, error) {
	rows, err := t.q.Query(t.ctx, `SELECT `+coreDelegationColumns+` FROM public.authority_core_delegations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Delegation, error) { return scanCoreDelegation(row) })
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, err
}

func (t coreTx) PutDelegation(d domain.Delegation) error {
	if err := t.mustWrite(); err != nil {
		return err
	}
	org, ok := asUUID(d.Organization)
	if !ok {
		return fmt.Errorf("delegation %s names organization %q, which is not one of this host's", d.ID, d.Organization)
	}
	raw, err := json.Marshal(storeScopes(d.Scopes))
	if err != nil {
		return err
	}
	_, err = t.q.Exec(t.ctx, `
		INSERT INTO public.authority_core_delegations (id, organization_id, grantor, module, purpose, scopes, revoked)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET organization_id = EXCLUDED.organization_id, grantor = EXCLUDED.grantor, module = EXCLUDED.module,
		       purpose = EXCLUDED.purpose, scopes = EXCLUDED.scopes, revoked = EXCLUDED.revoked`,
		d.ID, org, d.Grantor, d.Module, d.Purpose, raw, d.Revoked)
	return err
}

// --- single-use nonces ------------------------------------------------------

// AuthorityCoreReplay is core's replay store for the capabilities the core
// issues: one row per consumed nonce, shared by every replica.
func (s *PostgresStore) AuthorityCoreReplay() workcontext.ReplayStore {
	return AuthorityCoreReplay{s: s}
}

// AuthorityCoreReplay consumes a nonce once across every replica, judged by
// the database's clock; an expired consumption is reclaimed by the next one.
type AuthorityCoreReplay struct{ s *PostgresStore }

// Consume records nonce as spent until retainUntil, refusing it the second time.
func (r AuthorityCoreReplay) Consume(ctx context.Context, nonce string, retainUntil time.Time) error {
	recordControlPlane(ctx)
	return r.s.withControlPlaneTx(ctx, pgx.TxOptions{}, func(ctx context.Context) error {
		q := r.s.getQueryExecutor(ctx)
		if _, err := q.Exec(ctx, `DELETE FROM public.authority_core_replay WHERE nonce = $1 AND retain_until <= now()`, nonce); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, `INSERT INTO public.authority_core_replay (nonce, retain_until) VALUES ($1, $2) ON CONFLICT (nonce) DO NOTHING`, nonce, retainUntil)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: nonce %s", workcontext.ErrReplayed, nonce)
		}
		return nil
	})
}

// PurgeExpiredAuthorityReplays deletes the consumptions whose capability has expired.
func (r AuthorityCoreReplay) PurgeExpiredAuthorityReplays(ctx context.Context) (int64, error) {
	var purged int64
	err := r.s.WithControlPlane(ctx, func(ctx context.Context) error {
		tag, err := r.s.getQueryExecutor(ctx).Exec(ctx, `DELETE FROM public.authority_core_replay WHERE retain_until <= now()`)
		purged = tag.RowsAffected()
		return err
	})
	return purged, err
}
