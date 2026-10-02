package infra_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/infra/internal/txbind"
	"accounts/pkg/infra/storetx"
)

// ambientTx stands in for a transaction an outer helper opened. Within must
// decide whether to join it from its binding alone, before touching it or the
// store's pools, so the zero value is enough.
type ambientTx struct{ pgx.Tx }

// Scoped.Within joins a transaction already on the context only when that
// transaction carries exactly the identity's authority: System() joins a
// control-plane transaction, and a tenant or user identity a request
// transaction bound for exactly its own org and user. Joining any other would
// run the identity's work under the wrong authority — a tenant identity inside
// a control-plane transaction runs as app_control_plane with no org or user
// bound, and one inside a request transaction bound for another org reads that
// org's rows through any lookup by id.
func TestScopedWithinJoinsOnlyATransactionCarryingItsAuthority(t *testing.T) {
	const (
		orgA  = "11111111-1111-4111-8111-111111111111"
		orgB  = "33333333-3333-4333-8333-333333333333"
		userU = "22222222-2222-4222-8222-222222222222"
		userV = "44444444-4444-4444-8444-444444444444"
	)
	store := &infra.PostgresStore{}
	tenant := business.Identity{OrgID: orgA}
	user := business.Identity{UserID: userU}
	member := business.Identity{OrgID: orgA, UserID: userU}

	type bind func(context.Context, pgx.Tx) context.Context
	request := func(orgID, userID string) bind {
		return func(ctx context.Context, tx pgx.Tx) context.Context {
			return txbind.BindRequest(ctx, tx, orgID, userID)
		}
	}
	const (
		wrongPool  = "cannot join the "
		wrongScope = "it is bound for a different org or user"
	)
	for name, tc := range map[string]struct {
		identity business.Identity
		bind     bind
		refusal  string // "" when Within joins
	}{
		"tenant identity, request transaction for its org":              {tenant, request(orgA, ""), ""},
		"user identity, request transaction for its user":               {user, request("", userU), ""},
		"member identity, request transaction for its org and user":     {member, request(orgA, userU), ""},
		"System(), control-plane transaction":                           {business.System(), txbind.BindControlPlane, ""},
		"tenant identity, control-plane transaction":                    {tenant, txbind.BindControlPlane, wrongPool + "control-plane transaction"},
		"user identity, control-plane transaction":                      {user, txbind.BindControlPlane, wrongPool + "control-plane transaction"},
		"tenant identity, worker transaction":                           {tenant, txbind.BindWorker, wrongPool + "worker transaction"},
		"System(), request transaction":                                 {business.System(), request(orgA, ""), wrongPool + "request transaction"},
		"System(), worker transaction":                                  {business.System(), txbind.BindWorker, wrongPool + "worker transaction"},
		"tenant identity, request transaction for another org":          {tenant, request(orgB, ""), wrongScope},
		"user identity, request transaction for another user":           {user, request("", userV), wrongScope},
		"tenant identity, request transaction for its org and a user":   {tenant, request(orgA, userU), wrongScope},
		"member identity, request transaction for its org alone":        {member, request(orgA, ""), wrongScope},
		"member identity, request transaction for another user":         {member, request(orgA, userV), wrongScope},
		"user identity, request transaction for an org":                 {user, request(orgA, ""), wrongScope},
		"tenant identity, request transaction that bound no scope":      {tenant, request("", ""), wrongScope},
		"tenant identity, request transaction for another org's member": {tenant, request(orgB, userU), wrongScope},
	} {
		t.Run(name, func(t *testing.T) {
			outer := ambientTx{}
			ctx := tc.bind(context.Background(), outer)
			ran := false
			err := store.As(tc.identity).Within(ctx, func(ctx context.Context) error {
				ran = true
				require.Equal(t, pgx.Tx(outer), storetx.Tx(ctx), "Within joins the ambient transaction rather than opening its own")
				return nil
			})
			if tc.refusal == "" {
				require.NoError(t, err)
				require.True(t, ran)
				return
			}
			require.ErrorContains(t, err, tc.refusal)
			require.False(t, ran, "Within ran the identity's work inside a transaction carrying other authority")
		})
	}
}
