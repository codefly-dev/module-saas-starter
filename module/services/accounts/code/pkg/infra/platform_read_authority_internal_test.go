package infra

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Platform read authority may enter a decision only for a principal reading,
// on a request that is not impersonated. Every other combination is refused
// before the query consults platform_admins at all.
func TestPlatformReadAdmissibleFailsClosed(t *testing.T) {
	actor, subject := uuid.New(), uuid.New()
	ordinary := auth.WithVerifiedRequestIdentity(context.Background(), auth.RequestIdentity{RealActor: actor, EffectiveSubject: actor})
	impersonated := auth.WithVerifiedRequestIdentity(context.Background(), auth.RequestIdentity{RealActor: actor, EffectiveSubject: subject})
	principal, team := gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, gen.SubjectKind_SUBJECT_KIND_TEAM

	require.True(t, platformReadAdmissible(ordinary, principal, "read"))
	// No verified request identity: a Work Context read or a background fan-out.
	require.True(t, platformReadAdmissible(context.Background(), principal, "read"))

	require.False(t, platformReadAdmissible(impersonated, principal, "read"))
	require.False(t, platformReadAdmissible(ordinary, team, "read"))
	require.False(t, platformReadAdmissible(ordinary, gen.SubjectKind_SUBJECT_KIND_UNSPECIFIED, "read"))
	for _, action := range []string{"write", "delete", "*", "Read", ""} {
		require.False(t, platformReadAdmissible(ordinary, principal, action), action)
	}
}
