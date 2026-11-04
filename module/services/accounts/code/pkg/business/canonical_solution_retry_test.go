//go:build pure

package business

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

type canonicalRetryStore struct {
	Store
	plan SolutionInstallationPlan
}

func (s *canonicalRetryStore) SolutionInstallationPlan(context.Context, string, string) (SolutionInstallationPlan, error) {
	return s.plan, nil
}
func (*canonicalRetryStore) EnqueueCanonicalInstallationStep(context.Context, domain.Installation, bool) (string, error) {
	return "", errors.New("the test uses a step barrier, not a SQL queue")
}

func TestCanonicalRetryReauthorizesOriginalConsentBeforeReplaying(t *testing.T) {
	for _, change := range []string{"unchanged", "different administrator", "withdrawn", "revoked", "redelivered", "nonadministrator"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			store := memstore.New()
			store.PutOrganization(domain.Organization{ID: "org", Name: "Example"})
			for _, id := range []string{"approver", "other"} {
				store.PutPerson(domain.Person{ID: id, Organization: "org", Roles: []string{"admin"}})
			}
			store.PutPerson(domain.Person{ID: "member", Organization: "org"})
			module := domain.Module{Name: "example", Principal: "example-principal", Kind: domain.KindSolution, Generation: 1,
				Roles: map[string][]*basev0.WorkScopeV1{"machine": {{ResourceKind: "records", Actions: []string{"read"}}}}}
			require.NoError(t, store.Write(ctx, func(tx authority.Tx) error { return tx.PutModule(module) }))
			public, private, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			core, err := authority.New(store, authority.Config{Issuer: "test.example", KeyID: "key", Signer: private, Keys: staticKeys{"key": public}, Replay: workcontext.NewMemoryReplayStore()})
			require.NoError(t, err)
			caller := authority.Person{ID: "approver", Organization: "org"}
			approval, err := core.Review(ctx, caller, "org", "example")
			require.NoError(t, err)
			approval.OperationID = NewIDString()
			pending := errors.New("pending setup")
			inst, err := core.Install(ctx, caller, "org", "example", "machine", "", &approval,
				func(context.Context, domain.Installation) error { return pending })
			require.ErrorIs(t, err, pending)
			hostStore := &canonicalRetryStore{plan: SolutionInstallationPlan{InstallationID: inst.ID, Organization: "org", Target: "example",
				MachineRole: "machine", RequestedRole: "machine", Approval: approval, Revision: inst.Revision, Receipt: inst.Consent}}
			s, err := NewService(hostStore)
			require.NoError(t, err)
			s.authorityCore = &authorityCoreState{core: core}
			calls := 0
			s.SetAuthorityInstallStep(func(ctx context.Context, current domain.Installation) error {
				calls++
				require.Equal(t, true, ctx.Value(retryCanonicalSetupKey{}))
				require.Equal(t, inst.Consent, current.Consent)
				require.Equal(t, inst.Revision, current.Revision)
				return pending
			})
			switch change {
			case "different administrator":
				caller.ID = "other"
			case "nonadministrator":
				caller.ID = "member"
			case "revoked":
				require.NoError(t, core.Uninstall(ctx, caller, "org", inst.ID))
			case "withdrawn":
				require.NoError(t, core.WithdrawModule(ctx, module.Name))
			case "redelivered":
				module.Generation++
				require.NoError(t, store.Write(ctx, func(tx authority.Tx) error { return tx.PutModule(module) }))
			}
			_, err = s.RetryCanonicalSolution(ctx, caller, "org", inst.ID)
			require.Error(t, err)
			if change == "unchanged" {
				require.ErrorIs(t, err, pending)
				require.Equal(t, 1, calls)
			} else {
				require.Zero(t, calls, "stale or foreign approval must never reach queue replay")
			}
		})
	}
}
