package rolecatalog

import (
	"fmt"
	"sort"
	"strings"
)

// StateRole is one built-in role (roles.org_id IS NULL) as it currently exists
// in the database, with its permission set and — for catalog-managed roles —
// the number of assignments that would be cascaded away if it were removed.
type StateRole struct {
	ID              string
	Name            string
	Description     string
	Scope           string
	CatalogManaged  bool
	Permissions     []Permission
	AssignmentCount int
}

// State is the snapshot of all built-in roles the importer reasons about.
type State struct {
	Roles []StateRole
}

// RoleCreate is a role present in the catalog but not in the database.
type RoleCreate struct {
	Role Role
}

// RoleUpdate is a catalog role whose database row or permission set drifts from
// the catalog. Only the fields that actually changed are populated, so applying
// an update touches exactly the rows that differ.
type RoleUpdate struct {
	RoleID            string
	Name              string
	AdoptManaged      bool // flipping a non-catalog built-in into catalog management
	DescriptionChange *StringChange
	ScopeChange       *StringChange
	AddPermissions    []Permission
	RemovePermissions []Permission

	// DesiredDescription and DesiredScope are the catalog's target values,
	// carried so the apply path can write the roles row without re-reading the
	// catalog. They are always populated; the *Change pointers above drive
	// rendering and decide whether the row is written at all.
	DesiredDescription string
	DesiredScope       string
}

// RowChanged reports whether the roles row itself (not its permissions) drifts
// from the catalog and must be rewritten.
func (u RoleUpdate) RowChanged() bool {
	return u.AdoptManaged || u.DescriptionChange != nil || u.ScopeChange != nil
}

// StringChange records a from→to text change for deterministic rendering.
type StringChange struct {
	From string
	To   string
}

// RoleRemove is a catalog-managed role no longer present in the catalog.
// AssignmentCount is the number of role_assignments that a removal would cascade
// away; a non-zero count is what the orphan guard refuses without --force.
type RoleRemove struct {
	RoleID          string
	Name            string
	AssignmentCount int
	Permissions     []Permission
}

// Plan is the deterministic set of changes that reconciles the database toward a
// catalog. Sections are ordered by role name.
type Plan struct {
	Creates []RoleCreate
	Updates []RoleUpdate
	Removes []RoleRemove
}

// Empty reports whether applying the plan would change nothing.
func (p *Plan) Empty() bool {
	return len(p.Creates) == 0 && len(p.Updates) == 0 && len(p.Removes) == 0
}

// OrphaningRemovals returns the removals that would cascade away existing
// assignments. The importer refuses to apply these unless forced.
func (p *Plan) OrphaningRemovals() []RoleRemove {
	var out []RoleRemove
	for _, r := range p.Removes {
		if r.AssignmentCount > 0 {
			out = append(out, r)
		}
	}
	return out
}

// Diff computes the plan that reconciles current state toward the catalog.
//
// Ownership rules:
//   - A catalog role that doesn't exist as a built-in is created.
//   - A catalog role that exists is updated to match — and adopted into catalog
//     management if it was a hand-seeded built-in (e.g. admin) the catalog now
//     names.
//   - A catalog-managed built-in absent from the catalog is removed.
//   - A built-in that is neither catalog-managed nor named by the catalog is
//     left untouched, so the seed admin/editor/viewer roles survive a catalog
//     that doesn't mention them.
func Diff(state State, catalog *Catalog) *Plan {
	byName := make(map[string]StateRole, len(state.Roles))
	for _, r := range state.Roles {
		byName[r.Name] = r
	}

	plan := &Plan{}
	desired := make(map[string]struct{}, len(catalog.Roles))
	for _, want := range catalog.Roles {
		desired[want.Name] = struct{}{}
		current, exists := byName[want.Name]
		if !exists {
			plan.Creates = append(plan.Creates, RoleCreate{Role: want})
			continue
		}
		if update, changed := diffRole(current, want); changed {
			plan.Updates = append(plan.Updates, update)
		}
	}

	for _, current := range state.Roles {
		if !current.CatalogManaged {
			continue
		}
		if _, wanted := desired[current.Name]; wanted {
			continue
		}
		plan.Removes = append(plan.Removes, RoleRemove{
			RoleID:          current.ID,
			Name:            current.Name,
			AssignmentCount: current.AssignmentCount,
			Permissions:     current.Permissions,
		})
	}

	sort.Slice(plan.Creates, func(i, j int) bool { return plan.Creates[i].Role.Name < plan.Creates[j].Role.Name })
	sort.Slice(plan.Updates, func(i, j int) bool { return plan.Updates[i].Name < plan.Updates[j].Name })
	sort.Slice(plan.Removes, func(i, j int) bool { return plan.Removes[i].Name < plan.Removes[j].Name })
	return plan
}

func diffRole(current StateRole, want Role) (RoleUpdate, bool) {
	update := RoleUpdate{
		RoleID:             current.ID,
		Name:               current.Name,
		DesiredDescription: want.Description,
		DesiredScope:       want.Scope,
	}
	changed := false

	if !current.CatalogManaged {
		update.AdoptManaged = true
		changed = true
	}
	if current.Description != want.Description {
		update.DescriptionChange = &StringChange{From: current.Description, To: want.Description}
		changed = true
	}
	if current.Scope != want.Scope {
		update.ScopeChange = &StringChange{From: current.Scope, To: want.Scope}
		changed = true
	}

	add, remove := diffPermissions(current.Permissions, want.Permissions)
	if len(add) > 0 {
		update.AddPermissions = add
		changed = true
	}
	if len(remove) > 0 {
		update.RemovePermissions = remove
		changed = true
	}
	return update, changed
}

func diffPermissions(current, want []Permission) (add, remove []Permission) {
	have := make(map[Permission]struct{}, len(current))
	for _, p := range current {
		have[p] = struct{}{}
	}
	wanted := make(map[Permission]struct{}, len(want))
	for _, p := range want {
		wanted[p] = struct{}{}
	}
	for _, p := range want {
		if _, ok := have[p]; !ok {
			add = append(add, p)
		}
	}
	for _, p := range current {
		if _, ok := wanted[p]; !ok {
			remove = append(remove, p)
		}
	}
	sort.Slice(add, func(i, j int) bool { return less(add[i], add[j]) })
	sort.Slice(remove, func(i, j int) bool { return less(remove[i], remove[j]) })
	return add, remove
}

// Format renders the plan as a stable, human-readable diff. Output is
// byte-identical for an equivalent plan (consistent with the repo's
// byte-determinism convention) and does not depend on --force: whether a
// removal is refused is decided at apply time, not shown here.
func (p *Plan) Format() string {
	var b strings.Builder
	b.WriteString("role catalog plan\n\n")
	if p.Empty() {
		b.WriteString("no changes\n")
		return b.String()
	}

	for _, c := range p.Creates {
		fmt.Fprintf(&b, "create role %q\n", c.Role.Name)
		if c.Role.Scope != "" {
			fmt.Fprintf(&b, "  scope %s\n", c.Role.Scope)
		}
		if c.Role.Description != "" {
			fmt.Fprintf(&b, "  description %q\n", c.Role.Description)
		}
		for _, perm := range c.Role.Permissions {
			fmt.Fprintf(&b, "  + permission %s\n", perm)
		}
		b.WriteString("\n")
	}

	for _, u := range p.Updates {
		fmt.Fprintf(&b, "update role %q\n", u.Name)
		if u.AdoptManaged {
			b.WriteString("  adopt into catalog management\n")
		}
		if u.DescriptionChange != nil {
			fmt.Fprintf(&b, "  description %q -> %q\n", u.DescriptionChange.From, u.DescriptionChange.To)
		}
		if u.ScopeChange != nil {
			fmt.Fprintf(&b, "  scope %q -> %q\n", u.ScopeChange.From, u.ScopeChange.To)
		}
		for _, perm := range u.RemovePermissions {
			fmt.Fprintf(&b, "  - permission %s\n", perm)
		}
		for _, perm := range u.AddPermissions {
			fmt.Fprintf(&b, "  + permission %s\n", perm)
		}
		b.WriteString("\n")
	}

	for _, r := range p.Removes {
		fmt.Fprintf(&b, "remove role %q (%d assignment(s))\n", r.Name, r.AssignmentCount)
		for _, perm := range r.Permissions {
			fmt.Fprintf(&b, "  - permission %s\n", perm)
		}
		b.WriteString("\n")
	}

	return b.String()
}
