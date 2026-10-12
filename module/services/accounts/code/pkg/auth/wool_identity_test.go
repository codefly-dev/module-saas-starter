package auth_test

import (
	"context"
	"testing"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

// The verified door is the one place wool's identity is bound, so everything
// downstream — logs, traces, the rate-limit key, an organization-defaulted read
// — sees whose request this is without being handed it.
func TestVerifiedDatabaseIdentityPopulatesWool(t *testing.T) {
	user, org := uuid.NewString(), uuid.NewString()

	ctx := auth.WithVerifiedDatabaseIdentity(context.Background(), user, org)

	gotOrg, ok := wool.Get(ctx).OrgID()
	require.True(t, ok, "wool must carry the organization after the verified door")
	require.Equal(t, org, gotOrg)
	gotUser, ok := wool.Get(ctx).UserID()
	require.True(t, ok)
	require.Equal(t, user, gotUser)
}

// The invalid path returns the context untouched, so a malformed
// trusted-forwarding value cannot put an organization into wool either — the
// same fail-closed behaviour the verified identity already had.
func TestAMalformedIdentityPopulatesNothing(t *testing.T) {
	for name, identity := range map[string][2]string{
		"unparseable org":  {uuid.NewString(), "not-a-uuid"},
		"unparseable user": {"not-a-uuid", uuid.NewString()},
		"nil org":          {uuid.NewString(), uuid.Nil.String()},
		"nil user":         {uuid.Nil.String(), uuid.NewString()},
		"both empty":       {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := auth.WithVerifiedDatabaseIdentity(context.Background(), identity[0], identity[1])

			_, ok := wool.Get(ctx).OrgID()
			require.False(t, ok, "a malformed identity must leave wool empty")
			_, _, verified := auth.VerifiedDatabaseIdentity(ctx)
			require.False(t, verified)
		})
	}
}

// wool's organization is now read by things that matter (the rate-limit key, an
// organization-defaulted audit read), so it must only ever carry a verified
// value. This pins that the verified door is the ONLY writer: the key is
// exported, so anything may set it, and a second writer — especially one fed by
// caller-controlled input — would make wool's organization forgeable.
//
// It is deliberately NOT an authority source even so: pkg/auth states that
// transport headers and wool values are insufficient database or
// authorization-cache authority, and this does not change that. The point of one
// writer is that the observability dimension cannot be made to lie.
func TestOnlyTheVerifiedDoorWritesWoolIdentity(t *testing.T) {
	// Two writers, and each one's reason is why the list is pinned rather than
	// merely bounded:
	//
	//   database_identity.go — the verified door. Binds the organization (and
	//     the user) on every path that installs a verified identity, which is
	//     the only way wool's organization can be trusted at all.
	//   grpc_auth_interceptor.go — the user id only, unconditionally, because a
	//     verified user who has not selected an organization binds no database
	//     scope and so reaches no door above, yet should still be named in logs
	//     and in the rate-limit key.
	//
	// A THIRD writer is what this refuses. The keys are exported, so anything
	// may set them, and a writer fed by caller-controlled input would make the
	// dimension forgeable.
	writers := sourceSitesSettingWoolIdentity(t)
	require.Equal(t, []string{
		"pkg/adapters/grpc_auth_interceptor.go",
		"pkg/auth/database_identity.go",
	}, writers,
		"wool's identity keys have exactly two writers, both named above; a third makes the dimension "+
			"forgeable, and the rate limiter and the organization-defaulted audit read both consult it")
}
