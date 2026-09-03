package business

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/codefly-dev/core/wool"
)

// DashboardVisibility is how far a dashboard is visible inside its org.
// "private" is the owner (and org admins) only; "org" shares it read-only with
// every member.
type DashboardVisibility string

const (
	DashboardVisibilityPrivate DashboardVisibility = "private"
	DashboardVisibilityOrg     DashboardVisibility = "org"
)

const dashboardNameMaxLen = 200

// Dashboard is the domain representation of a user-owned dashboard. Spec is the
// canonical JSON of a DashboardDef; org_id and owner_id are server-assigned.
type Dashboard struct {
	ID         string
	OrgID      string
	OwnerID    string
	Name       string
	Spec       []byte
	Visibility DashboardVisibility
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DashboardListScope selects which slice of the visible collection a list call
// returns.
type DashboardListScope int

const (
	// DashboardListAll returns everything the caller may see: their own plus
	// every org-shared board.
	DashboardListAll DashboardListScope = iota
	// DashboardListMine returns caller-owned boards only, at any visibility.
	DashboardListMine
	// DashboardListOrgShared returns org-shared boards, whoever owns them.
	DashboardListOrgShared
)

const (
	defaultDashboardPageSize = 50
	maxDashboardPageSize     = 100
)

// DashboardCursor is a keyset position in the (created_at, id) descending order
// a list uses to page. created_at is immutable, so a row cannot move across the
// cursor when another writer bumps its updated_at — a page walk therefore never
// skips a live row. It is opaque to clients, carried as page_token.
type DashboardCursor struct {
	CreatedAt time.Time
	ID        string
}

const dashboardCursorSep = "\x1f"

func clampDashboardPageSize(n int) int {
	if n <= 0 {
		return defaultDashboardPageSize
	}
	if n > maxDashboardPageSize {
		return maxDashboardPageSize
	}
	return n
}

func encodeDashboardCursor(c *DashboardCursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + dashboardCursorSep + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeDashboardCursor(token string) (*DashboardCursor, error) {
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	createdAt, id, ok := strings.Cut(string(raw), dashboardCursorSep)
	if !ok {
		return nil, fmt.Errorf("malformed dashboard cursor")
	}
	ts, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	// The id becomes a uuid-typed query parameter; reject a non-uuid cursor here
	// so a crafted token fails as InvalidArgument, not a Postgres cast error
	// surfaced as Internal.
	if _, err := uuid.Parse(id); err != nil {
		return nil, err
	}
	return &DashboardCursor{CreatedAt: ts, ID: id}, nil
}

// assertDashboardName rejects an empty or whitespace-only name at the write
// boundary — a name is what a user picks a dashboard out of a list by. The DB
// CHECK is the backstop; this returns a caller-facing code.
func assertDashboardName(name string) error {
	if strings.TrimSpace(name) == "" {
		return status.Error(codes.InvalidArgument, "a dashboard needs a name")
	}
	if len(name) > dashboardNameMaxLen {
		return status.Error(codes.InvalidArgument, "dashboard name is too long")
	}
	return nil
}

// assertDashboardSpec validates the spec at the write boundary. The full
// DashboardDef DSL — that it compiles only to org-scoped, read-only audit
// queries — is validated client-side by assertDashboardSpec and, on the way to
// a query, by the SDK compile guard (#367). Here the server enforces the
// storable invariant it owns: a spec is a non-empty JSON object.
func assertDashboardSpec(spec []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(spec, &probe); err != nil {
		return status.Error(codes.InvalidArgument, "dashboard spec must be a JSON object")
	}
	if len(probe) == 0 {
		return status.Error(codes.InvalidArgument, "dashboard spec must not be empty")
	}
	return nil
}

// CreateDashboard persists a new user-owned dashboard. A new board is always
// private; promotion to org visibility is ShareDashboard, the privileged
// transition. owner_id is the actor and org_id the caller's org — never trusted
// from the client. A non-empty id is honored for an idempotent create.
func (s *Service) CreateDashboard(ctx context.Context, orgID, ownerID, id, name string, spec []byte) (*Dashboard, error) {
	w := wool.Get(ctx).In("CreateDashboard")

	if err := assertDashboardName(name); err != nil {
		return nil, err
	}
	if err := assertDashboardSpec(spec); err != nil {
		return nil, err
	}
	if id == "" {
		id = NewIDString()
	}

	record := &Dashboard{
		ID:         id,
		OrgID:      orgID,
		OwnerID:    ownerID,
		Name:       name,
		Spec:       spec,
		Visibility: DashboardVisibilityPrivate,
	}

	var stored *Dashboard
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		stored, err = s.store.CreateDashboard(ctx, record)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create dashboard")
	}

	s.emit(ctx, ownerID, "user", EventDashboardCreated, "dashboard", id, orgID)
	return stored, nil
}

// GetDashboard returns one dashboard the caller may read: their own, or an
// org-shared one. A board the caller cannot read is reported as absent rather
// than forbidden, so its existence never leaks.
func (s *Service) GetDashboard(ctx context.Context, orgID, actorID string, isAdmin bool, id string) (*Dashboard, error) {
	record, err := s.loadDashboard(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !canReadDashboard(record, actorID, isAdmin) {
		return nil, status.Error(codes.NotFound, "dashboard not found")
	}
	return record, nil
}

// ListDashboards returns one bounded page of the caller's visible collection
// within their org, newest activity first, plus the token for the next page (or
// "" when the page is the last). A page is always bounded so a member with an
// arbitrarily large collection can never force an unbounded read.
func (s *Service) ListDashboards(ctx context.Context, orgID, actorID string, scope DashboardListScope, pageSize int, pageToken string) ([]*Dashboard, string, error) {
	w := wool.Get(ctx).In("ListDashboards")

	pageSize = clampDashboardPageSize(pageSize)
	cursor, err := decodeDashboardCursor(pageToken)
	if err != nil {
		return nil, "", status.Error(codes.InvalidArgument, "invalid page token")
	}

	var records []*Dashboard
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		// One extra row tells us whether a further page exists without a count.
		records, err = s.store.ListDashboards(ctx, orgID, actorID, scope, pageSize+1, cursor)
		return err
	}); err != nil {
		return nil, "", w.Wrapf(err, "cannot list dashboards")
	}

	next := ""
	if len(records) > pageSize {
		records = records[:pageSize]
		last := records[len(records)-1]
		next = encodeDashboardCursor(&DashboardCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return records, next, nil
}

// UpdateDashboard applies a name and/or spec change. Only the owner or an org
// admin may edit; visibility is untouched (see ShareDashboard).
func (s *Service) UpdateDashboard(ctx context.Context, orgID, actorID string, isAdmin bool, id string, name *string, spec []byte) (*Dashboard, error) {
	w := wool.Get(ctx).In("UpdateDashboard")

	if name != nil {
		if err := assertDashboardName(*name); err != nil {
			return nil, err
		}
	}
	if spec != nil {
		if err := assertDashboardSpec(spec); err != nil {
			return nil, err
		}
	}

	var stored *Dashboard
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		record, err := s.store.GetDashboard(ctx, id)
		if err != nil {
			return w.Wrapf(err, "cannot load dashboard")
		}
		if record == nil {
			return status.Error(codes.NotFound, "dashboard not found")
		}
		if !canEditDashboard(record, actorID, isAdmin) {
			return status.Error(codes.PermissionDenied, "not authorized to edit this dashboard")
		}
		stored, err = s.store.UpdateDashboard(ctx, id, name, spec)
		if err != nil {
			return err
		}
		// A concurrent delete (READ COMMITTED) can remove the row between the
		// load above and this UPDATE, leaving a zero-row RETURNING. Report it as
		// gone rather than returning a nil record the caller would deref.
		if stored == nil {
			return status.Error(codes.NotFound, "dashboard not found")
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.emit(ctx, actorID, "user", EventDashboardUpdated, "dashboard", id, orgID)
	return stored, nil
}

// DeleteDashboard removes a dashboard. Only the owner or an org admin may.
func (s *Service) DeleteDashboard(ctx context.Context, orgID, actorID string, isAdmin bool, id string) error {
	w := wool.Get(ctx).In("DeleteDashboard")

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		record, err := s.store.GetDashboard(ctx, id)
		if err != nil {
			return w.Wrapf(err, "cannot load dashboard")
		}
		if record == nil {
			return status.Error(codes.NotFound, "dashboard not found")
		}
		if !canEditDashboard(record, actorID, isAdmin) {
			return status.Error(codes.PermissionDenied, "not authorized to delete this dashboard")
		}
		return s.store.DeleteDashboard(ctx, id)
	}); err != nil {
		return err
	}

	s.emit(ctx, actorID, "user", EventDashboardDeleted, "dashboard", id, orgID)
	return nil
}

// ShareDashboard sets a dashboard's visibility. Promotion to org is the
// privileged transition (dashboards:share, org-admin) enforced at the handler.
func (s *Service) ShareDashboard(ctx context.Context, orgID, actorID string, id string, visibility DashboardVisibility) (*Dashboard, error) {
	w := wool.Get(ctx).In("ShareDashboard")

	if visibility != DashboardVisibilityPrivate && visibility != DashboardVisibilityOrg {
		return nil, status.Error(codes.InvalidArgument, "invalid dashboard visibility")
	}

	var stored *Dashboard
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		record, err := s.store.GetDashboard(ctx, id)
		if err != nil {
			return w.Wrapf(err, "cannot load dashboard")
		}
		if record == nil {
			return status.Error(codes.NotFound, "dashboard not found")
		}
		stored, err = s.store.SetDashboardVisibility(ctx, id, visibility)
		if err != nil {
			return err
		}
		// A concurrent delete (READ COMMITTED) can remove the row between the
		// load above and this UPDATE, leaving a zero-row RETURNING. Report it as
		// gone rather than returning a nil record the caller would deref.
		if stored == nil {
			return status.Error(codes.NotFound, "dashboard not found")
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.emit(ctx, actorID, "user", EventDashboardShared, "dashboard", id, orgID)
	return stored, nil
}

func (s *Service) loadDashboard(ctx context.Context, orgID, id string) (*Dashboard, error) {
	w := wool.Get(ctx).In("loadDashboard")

	var record *Dashboard
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		record, err = s.store.GetDashboard(ctx, id)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot load dashboard")
	}
	if record == nil {
		return nil, status.Error(codes.NotFound, "dashboard not found")
	}
	return record, nil
}

func canReadDashboard(record *Dashboard, actorID string, isAdmin bool) bool {
	return record.OwnerID == actorID || record.Visibility == DashboardVisibilityOrg || isAdmin
}

func canEditDashboard(record *Dashboard, actorID string, isAdmin bool) bool {
	return record.OwnerID == actorID || isAdmin
}
