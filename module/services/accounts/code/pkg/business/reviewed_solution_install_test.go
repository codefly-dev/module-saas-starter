//go:build pure

package business

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type reviewedInstallStore struct {
	Store
	targets []*AvailableSolutionTarget
	records map[string]*SolutionHostBindingRecord
	plan    SolutionInstallationPlan
}

func (s *reviewedInstallStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *reviewedInstallStore) ListAvailableSolutionTargets(ctx context.Context, q AvailableSolutionQuery) ([]*AvailableSolutionTarget, error) {
	return (availableInstallationStore{targets: s.targets}).ListAvailableSolutionTargets(ctx, q)
}
func (s *reviewedInstallStore) GetSolutionHostBindingForUpdate(_ context.Context, id string) (*SolutionHostBindingRecord, error) {
	return s.records[id], nil
}
func (s *reviewedInstallStore) SolutionInstallationPlan(context.Context, string, string) (SolutionInstallationPlan, error) {
	return s.plan, nil
}
func (*reviewedInstallStore) EnqueueCanonicalInstallationStep(context.Context, domain.Installation, bool) (string, error) {
	panic("pure review proof must not claim SQL queue execution")
}

func reviewedInstallFixture(t *testing.T, dependencies bool) (*Service, *reviewedInstallStore, *memstore.Store, authority.Person, string) {
	t.Helper()
	ctx := context.Background()
	org, actor, targetID := NewIDString(), NewIDString(), NewIDString()
	owner := memstore.New()
	owner.PutOrganization(domain.Organization{ID: org, Name: "Example"})
	owner.PutPerson(domain.Person{ID: actor, Organization: org, Roles: []string{"admin"}})
	module := domain.Module{Name: "example", Principal: "solution:example", Kind: domain.KindSolution, Generation: 1, InstallationRole: "none"}
	if dependencies {
		module.EdgeDependencies = []domain.EdgeDependency{{Module: "module:records", Presence: "required"}, {Module: "module:optional", Presence: "optional"}}
	}
	require.NoError(t, owner.Write(ctx, func(tx authority.Tx) error {
		if err := tx.PutModule(module); err != nil {
			return err
		}
		return tx.PutModule(domain.Module{Name: "records-route", Principal: "module:records", Kind: domain.KindModule, Generation: 3, InstallationRole: "none"})
	}))
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	core, err := authority.New(owner, authority.Config{Issuer: "test.example", KeyID: "key", Signer: private, Keys: staticKeys{"key": public}, Replay: workcontext.NewMemoryReplayStore()})
	require.NoError(t, err)
	host := &reviewedInstallStore{targets: []*AvailableSolutionTarget{
		{TargetID: targetID, BindingID: "example-binding", RouteAlias: "example", Kind: "solution", AppliedGeneration: 1},
		{TargetID: NewIDString(), BindingID: "records-binding", RouteAlias: "records-route", Kind: "module", AppliedGeneration: 3},
	}, records: map[string]*SolutionHostBindingRecord{}}
	registry := ModulePrincipalRegistry{}
	for i, target := range host.targets {
		principal := []string{"solution:example", "module:records"}[i]
		registry[ModulePrincipalID(principal)] = ModulePrincipalGrant{Prefix: principal, PresenceBinding: target.BindingID, PresenceGeneration: target.AppliedGeneration}
		host.records[target.BindingID] = &SolutionHostBindingRecord{BindingID: target.BindingID, Applied: &SolutionHostBindingApplied{SolutionHostBindingGeneration: SolutionHostBindingGeneration{Generation: target.AppliedGeneration, Digest: "digest-" + principal}, Release: "acme.example/example@1.0.0"}}
	}
	s := &Service{store: host, authorityCore: &authorityCoreState{core: core}}
	s.SetModulePrincipals(registry)
	return s, host, owner, authority.Person{ID: actor, Organization: org}, targetID
}

func TestReviewedSolutionAPIKeepsOriginalApprovalAcrossPendingAndActiveReplay(t *testing.T) {
	s, host, _, caller, target := reviewedInstallFixture(t, false)
	ctx := context.Background()
	review, err := s.ReviewSolutionInstall(ctx, caller, caller.Organization, target)
	require.NoError(t, err)
	require.Equal(t, "none", review.InstallationRole)
	require.NotEqual(t, review.TargetId, review.Approval.TargetId)
	acknowledged := false
	s.SetAuthorityInstallStep(func(ctx context.Context, inst domain.Installation) error {
		if plan, ok := SolutionInstallationPlanFromContext(ctx); ok {
			plan.InstallationID, plan.Revision, plan.Receipt = inst.ID, inst.Revision, inst.Consent
			host.plan = plan
		}
		if !acknowledged {
			return errors.New("receiver has not acknowledged")
		}
		return nil
	})
	pending, err := s.InstallReviewedSolution(ctx, caller, caller.Organization, review)
	require.ErrorContains(t, err, "receiver has not acknowledged")
	require.Equal(t, domain.Pending, pending.Status)
	require.True(t, proto.Equal(review, host.plan.Review))
	changed := proto.Clone(review).(*gen.ReviewedSolutionInstall)
	changed.DeliveryDigest = "another-review"
	_, err = s.InstallReviewedSolution(ctx, caller, caller.Organization, changed)
	require.ErrorContains(t, err, "different original review")
	acknowledged = true
	active, err := s.InstallReviewedSolution(ctx, caller, caller.Organization, review)
	require.NoError(t, err)
	require.Equal(t, domain.Active, active.Status)
	require.Equal(t, pending.Consent, active.Consent)
	require.Equal(t, pending.Revision, active.Revision)
	replayed, err := s.InstallReviewedSolution(ctx, caller, caller.Organization, review)
	require.NoError(t, err)
	require.Equal(t, active, replayed)
}

func TestReviewedSolutionAPIRefusesTamperingOrStaleDeliveryBeforeAnyStep(t *testing.T) {
	for _, change := range []string{"machine role", "target", "digest", "actor", "module scope", "module approval", "delivery"} {
		t.Run(change, func(t *testing.T) {
			s, host, _, caller, target := reviewedInstallFixture(t, true)
			ctx := context.Background()
			review, err := s.ReviewSolutionInstall(ctx, caller, caller.Organization, target)
			require.NoError(t, err)
			require.Len(t, review.Modules, 2)
			var module *gen.ReviewedSupportingModule
			for _, m := range review.Modules {
				if m.Required {
					module = m
				} else {
					require.Empty(t, m.Scope)
					require.Empty(t, m.TargetId)
				}
			}
			require.NotNil(t, module)
			require.Equal(t, "none", module.InstallationRole)
			require.JSONEq(t, `{"capabilities":true}`, module.Scope)
			switch change {
			case "machine role":
				review.InstallationRole = "admin"
			case "target":
				review.TargetId = NewIDString()
			case "digest":
				review.Approval.Digest = "changed"
			case "actor":
				review.ActorId = NewIDString()
			case "module scope":
				module.Scope = `{"capabilities":false}`
			case "module approval":
				module.Approval.OperationId = NewIDString()
			case "delivery":
				host.records["records-binding"].Applied.Digest = "redelivered"
			}
			calls := 0
			s.SetAuthorityInstallStep(func(context.Context, domain.Installation) error { calls++; return nil })
			_, err = s.InstallReviewedSolution(ctx, caller, caller.Organization, review)
			require.Error(t, err)
			require.Zero(t, calls)
		})
	}
}

func TestReviewedSolutionAPIRequiresDeclaredAuthorityWithoutChoosingRole(t *testing.T) {
	for _, selector := range []string{"", "reader"} {
		s, _, owner, caller, target := reviewedInstallFixture(t, true)
		require.NoError(t, owner.Write(context.Background(), func(tx authority.Tx) error {
			m, err := tx.ModuleNamed("records-route")
			if err != nil {
				return err
			}
			m.InstallationRole = selector
			return tx.PutModule(m)
		}))
		_, err := s.ReviewSolutionInstall(context.Background(), caller, caller.Organization, target)
		require.ErrorContains(t, err, "module:records")
	}
}

func TestFreshReviewedConsentSupersedesPendingWithoutReusingOldAttempt(t *testing.T) {
	s, host, _, caller, target := reviewedInstallFixture(t, false)
	ctx := context.Background()
	s.SetAuthorityInstallStep(func(ctx context.Context, inst domain.Installation) error {
		if plan, ok := SolutionInstallationPlanFromContext(ctx); ok {
			plan.InstallationID, plan.Revision, plan.Receipt = inst.ID, inst.Revision, inst.Consent
			host.plan = plan
		}
		return errors.New("awaiting setup")
	})
	first, err := s.ReviewSolutionInstall(ctx, caller, caller.Organization, target)
	require.NoError(t, err)
	old, err := s.InstallReviewedSolution(ctx, caller, caller.Organization, first)
	require.ErrorContains(t, err, "awaiting setup")
	fresh, err := s.ReviewSolutionInstall(ctx, caller, caller.Organization, target)
	require.NoError(t, err)
	require.NotEqual(t, first.Approval.OperationId, fresh.Approval.OperationId)
	require.Equal(t, old.Revision, fresh.Approval.ExpectedRevision)
	next, err := s.InstallReviewedSolution(ctx, caller, caller.Organization, fresh)
	require.ErrorContains(t, err, "awaiting setup")
	require.Equal(t, old.ID, next.ID)
	require.Greater(t, next.Revision, old.Revision)
	require.NotEqual(t, old.Consent, next.Consent)
	require.Equal(t, fresh.Approval.OperationId, host.plan.Receipt.OperationID)
	_, err = s.InstallReviewedSolution(ctx, caller, caller.Organization, first)
	require.Error(t, err, "old approval cannot acknowledge the superseding attempt")
}

func TestSingleTargetAdapterRetainsExplicitLegacyConsent(t *testing.T) {
	s, host, owner, caller, target := reviewedInstallFixture(t, false)
	ctx := context.Background()
	require.NoError(t, owner.Write(ctx, func(tx authority.Tx) error {
		m, err := tx.ModuleNamed("example")
		if err != nil {
			return err
		}
		m.InstallationRole = ""
		m.Roles = map[string][]*basev0.WorkScopeV1{"reader": {{ResourceKind: "records", Actions: []string{"read"}}}}
		return tx.PutModule(m)
	}))
	core, err := s.AuthorityCore()
	require.NoError(t, err)
	approval, err := core.Review(ctx, caller, caller.Organization, "example")
	require.NoError(t, err)
	approval.OperationID = NewIDString()
	acknowledged := false
	calls := 0
	s.SetAuthorityInstallStep(func(ctx context.Context, inst domain.Installation) error {
		calls++
		if p, ok := SolutionInstallationPlanFromContext(ctx); ok {
			p.InstallationID, p.Revision, p.Receipt = inst.ID, inst.Revision, inst.Consent
			host.plan = p
		}
		if !acknowledged {
			return errors.New("response lost before ACK")
		}
		return nil
	})
	pending, err := s.InstallAuthoritySolution(ctx, caller, caller.Organization, "example", "reader", "", &approval)
	require.ErrorContains(t, err, "response lost")
	require.Equal(t, target, host.plan.HostTargetID)
	require.Equal(t, "reader", host.plan.RequestedRole)
	require.Empty(t, host.plan.RequestedOwner)
	for _, changed := range []struct{ role, owner string }{{"", ""}, {"reader", caller.ID}, {"none", ""}} {
		_, err = s.InstallAuthoritySolution(ctx, caller, caller.Organization, "example", changed.role, changed.owner, &approval)
		require.Error(t, err)
	}
	require.Equal(t, 1, calls)
	acknowledged = true
	active, err := s.InstallAuthoritySolution(ctx, caller, caller.Organization, "example", "reader", "", &approval)
	require.NoError(t, err)
	require.Equal(t, pending.Consent, active.Consent)
	require.Equal(t, pending.Revision, active.Revision)
	_, err = s.InstallAuthoritySolution(ctx, caller, caller.Organization, "example", "reader", "", &approval)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

func TestSingleTargetAdapterCannotCreateSupportingConsent(t *testing.T) {
	s, _, _, caller, _ := reviewedInstallFixture(t, true)
	ctx := context.Background()
	core, err := s.AuthorityCore()
	require.NoError(t, err)
	a, err := core.Review(ctx, caller, caller.Organization, "example")
	require.NoError(t, err)
	a.OperationID = NewIDString()
	_, err = s.InstallAuthoritySolution(ctx, caller, caller.Organization, "example", "", "", &a)
	require.ErrorContains(t, err, "module:records")
	all, err := core.Installations(ctx, caller, caller.Organization)
	require.NoError(t, err)
	require.Empty(t, all)
}

func TestPlanFreezeClonesOnlyItsReferenceFields(t *testing.T) {
	p := SolutionInstallationPlan{Dependencies: []ModuleInstallationPrerequisite{{InstallationID: "original"}}, Review: &gen.ReviewedSolutionInstall{Modules: []*gen.ReviewedSupportingModule{{Principal: "original"}}}}
	ctx := WithSolutionInstallationPlan(context.Background(), p)
	p.Dependencies[0].InstallationID = "changed"
	p.Review.Modules[0].Principal = "changed"
	frozen, ok := SolutionInstallationPlanFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "original", frozen.Dependencies[0].InstallationID)
	require.Equal(t, "original", frozen.Review.Modules[0].Principal)
	empty, _ := SolutionInstallationPlanFromContext(WithSolutionInstallationPlan(context.Background(), SolutionInstallationPlan{}))
	require.Nil(t, empty.Dependencies)
	require.Nil(t, empty.Review)
}
