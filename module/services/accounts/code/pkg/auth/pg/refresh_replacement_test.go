package pgauth

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

// r1/f5. A rotation reissues the audience the authorization bound, so the
// replacement's resource must equal the locked row's.
//
// The projection that builds the replacement carries the field today, which is
// why this is a guard rather than a bug fix: it is what stops a later edit to
// that projection from silently unbinding a narrowed credential. An unbound
// credential is admitted wherever a resource-bound one is refused, so losing the
// binding widens the token rather than breaking it — the failure mode that does
// not announce itself.
//
// This is the first test in this package; validateRefreshReplacement is a pure
// function over two records and needs no database.
func TestARotationMustPreserveTheResourceBinding(t *testing.T) {
	const resource = "https://app.example.com/api/solutions/example/proxy/mcp"
	current, next, authorization := rotatablePair(resource)

	// The baseline pair is accepted, so every assertion below is about the one
	// field it changes rather than about an unrelated check firing first.
	require.NoError(t, validateRefreshReplacement(current, next, authorization))

	for name, mutate := range map[string]func(*auth.SessionRecord){
		"unbound":        func(r *auth.SessionRecord) { r.Resource = "" },
		"other solution": func(r *auth.SessionRecord) { r.Resource = "https://app.example.com/api/solutions/other/proxy/mcp" },
	} {
		t.Run(name, func(t *testing.T) {
			// A COPY of the accepted replacement, so the only difference from
			// the pair that passed above is the field this case changes.
			changed := *next
			mutate(&changed)
			err := validateRefreshReplacement(current, &changed, authorization)
			require.Error(t, err)
			require.Contains(t, err.Error(), "changed resource")
		})
	}

	// And a session that never named a resource rotates normally: the rule is
	// equality, not presence.
	unboundCurrent, unboundNext, unboundAuth := rotatablePair("")
	require.NoError(t, validateRefreshReplacement(unboundCurrent, unboundNext, unboundAuth))
}

// rotatablePair is a current/next pair that validateRefreshReplacement accepts,
// so a test can change exactly one field and know what refused it.
func rotatablePair(resource string) (*auth.SessionRecord, *auth.SessionRecord, auth.RefreshAuthorization) {
	user := uuid.New()
	family := uuid.New()
	org := uuid.New()
	issued := time.Now().Add(-time.Hour)
	expires := issued.Add(24 * time.Hour)
	active := time.Now()

	authorization := auth.RefreshAuthorization{
		OrgID:        org,
		OrgRole:      "member",
		PlatformRole: "",
	}
	base := func(id uuid.UUID, hash []byte) *auth.SessionRecord {
		return &auth.SessionRecord{
			ID:            id,
			UserID:        user,
			FamilyID:      family,
			OrgID:         org,
			OrgRole:       "member",
			RefreshHash:   hash,
			Resource:      resource,
			ClientID:      "example-cli",
			IssuedAt:      issued,
			ExpiresAt:     expires,
			LastActiveAt:  active,
			IdleExpiresAt: active.Add(time.Hour),
			DeviceInfo:    map[string]string{"ua": "test"},
			IPAddress:     "203.0.113.1",
		}
	}
	return base(uuid.New(), []byte("current-hash")),
		base(uuid.New(), []byte("next-hash")),
		authorization
}
