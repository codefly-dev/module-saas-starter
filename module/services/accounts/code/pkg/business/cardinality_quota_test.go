package business_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/fixtures"
	"accounts/pkg/adapters"
	"accounts/pkg/business"
	"accounts/pkg/cache"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

type fixtureMembershipFailureStore struct {
	business.Store
	err error
}

func (s *fixtureMembershipFailureStore) AddOrgMember(context.Context, string, string, string) error {
	return s.err
}

func TestFixtureSeedingBypassesSeatQuota(t *testing.T) {
	clearData(t)
	ctx := testCtx
	fixturePath := filepath.Join(t.TempDir(), "zero-seats.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)

	writeFixture := func(members string) {
		t.Helper()
		contents := fmt.Sprintf(`users:
  - email: owner@fixture.test
    provider: email
    provider_id: fixture-quota-owner
  - email: alice@fixture.test
    provider: email
    provider_id: fixture-quota-alice
  - email: bob@fixture.test
    provider: email
    provider_id: fixture-quota-bob
  - email: carol@fixture.test
    provider: email
    provider_id: fixture-quota-carol
organizations:
  - name: Fixture Quota Org
    owner: owner@fixture.test
%s`, members)
		require.NoError(t, os.WriteFile(fixturePath, []byte(contents), 0o600))
	}

	writeFixture("")
	require.NoError(t, fixtures.Seed(ctx, testService, "zero-seats"))

	var owner *gen.User
	var fixtureMemberIDs []string
	var organizations []*gen.Organization
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		owner, err = testStore.GetUserByIdentity(ctx, &gen.UserIdentity{
			Provider: "email", ProviderId: "fixture-quota-owner",
		})
		if err != nil {
			return err
		}
		for _, providerID := range []string{"fixture-quota-alice", "fixture-quota-bob", "fixture-quota-carol"} {
			member, lookupErr := testStore.GetUserByIdentity(ctx, &gen.UserIdentity{
				Provider: "email", ProviderId: providerID,
			})
			if lookupErr != nil {
				return lookupErr
			}
			fixtureMemberIDs = append(fixtureMemberIDs, member.Uuid)
		}
		organizations, err = testStore.ListOrganizationsForUser(ctx, owner.Uuid)
		return err
	}))
	require.Len(t, organizations, 1)
	orgID := organizations[0].Id
	setEntitlementLimit(t, ctx, orgID, owner.Uuid, business.EntitlementSeats, 0)
	membershipCache := cache.NewOrgMembershipCache(cache.NewMemory())
	adapters.WithOrgMembershipCache(membershipCache)
	testService.SetMembershipInvalidator(adapters.NewCacheInvalidator())
	t.Cleanup(func() {
		testService.SetMembershipInvalidator(nil)
		adapters.WithOrgMembershipCache(nil)
	})
	for _, memberID := range fixtureMemberIDs {
		require.NoError(t, membershipCache.Set(ctx, orgID, memberID, &cache.OrgMembership{}))
	}

	writeFixture(`    members:
      - email: alice@fixture.test
        role: admin
      - email: bob@fixture.test
        role: member
      - email: carol@fixture.test
        role: member
`)
	require.NoError(t, fixtures.Seed(ctx, testService, "zero-seats"))

	var members []*gen.OrgMembership
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		members, err = testStore.ListOrgMembers(ctx, orgID)
		return err
	}))
	require.Len(t, members, 4)
	for _, memberID := range fixtureMemberIDs {
		_, err := membershipCache.Get(ctx, orgID, memberID)
		require.ErrorIs(t, err, cache.ErrNotFound)
	}
}

func TestFixtureSeedingFailsOnMembershipWriteError(t *testing.T) {
	clearData(t)
	fixturePath := filepath.Join(t.TempDir(), "membership-failure.yaml")
	t.Setenv("DEV_FIXTURE_PATH", fixturePath)
	require.NoError(t, os.WriteFile(fixturePath, []byte(`users:
  - email: owner@fixture.test
    provider: email
    provider_id: fixture-failure-owner
  - email: member@fixture.test
    provider: email
    provider_id: fixture-failure-member
organizations:
  - name: Fixture Failure Org
    owner: owner@fixture.test
    members:
      - email: member@fixture.test
        role: member
`), 0o600))

	writeErr := errors.New("membership write unavailable")
	service, err := business.NewService(&fixtureMembershipFailureStore{
		Store: testStore,
		err:   writeErr,
	})
	require.NoError(t, err)

	err = fixtures.Seed(testCtx, service, "membership-failure")
	require.ErrorIs(t, err, writeErr)
	require.ErrorContains(t, err, "member@fixture.test")
}

func TestSeatQuotaSerializesDirectMemberAdmission(t *testing.T) {
	clearData(t)
	ctx := testCtx
	ownerID, orgID := mustUserAndOrg(t, ctx, "quota-owner@test.invalid", "quota-owner", "Quota Org")
	candidateA, _ := mustUserAndOrg(t, ctx, "quota-a@test.invalid", "quota-a", "Candidate A")
	candidateB, _ := mustUserAndOrg(t, ctx, "quota-b@test.invalid", "quota-b", "Candidate B")
	setEntitlementLimit(t, ctx, orgID, ownerID, business.EntitlementSeats, 2)

	errs := runConcurrently(2, func(index int) error {
		candidate := []string{candidateA, candidateB}[index]
		return testService.AddOrgMember(ctx, ownerID, &gen.AddOrgMemberRequest{
			OrgId: orgID, UserId: candidate, Role: gen.OrgRole_ORG_ROLE_MEMBER,
		})
	})
	requireOneQuotaRejection(t, errs)

	var members []*gen.OrgMembership
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		members, err = testStore.ListOrgMembers(ctx, orgID)
		return err
	}))
	require.Len(t, members, 2, "owner plus exactly one concurrent candidate")

	// A role update for the already-admitted member is idempotent and must not
	// require another seat merely because the organization is now full.
	var admitted string
	for _, member := range members {
		if member.UserId != ownerID {
			admitted = member.UserId
		}
	}
	require.NotEmpty(t, admitted)
	require.NoError(t, testService.AddOrgMember(ctx, ownerID, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: admitted, Role: gen.OrgRole_ORG_ROLE_ADMIN,
	}))
}

func TestSeatQuotaSerializesInvitationReservations(t *testing.T) {
	clearData(t)
	ctx := testCtx
	ownerID, orgID := mustUserAndOrg(t, ctx, "invite-owner@test.invalid", "invite-owner", "Invite Quota Org")
	setEntitlementLimit(t, ctx, orgID, ownerID, business.EntitlementSeats, 2)

	errs := runConcurrently(2, func(index int) error {
		_, err := testService.CreateInvitation(ctx, ownerID, &gen.CreateInvitationRequest{
			OrgId: orgID, Email: fmt.Sprintf("candidate-%d@test.invalid", index), Role: gen.InvitationRole_INVITATION_ROLE_MEMBER,
		})
		return err
	})
	requireOneQuotaRejection(t, errs)

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		pending, err := testStore.CountPendingInvitations(ctx, orgID)
		require.NoError(t, err)
		require.Equal(t, int32(1), pending)
		return nil
	}))
}

func TestAPIKeyQuotaSerializesAdmission(t *testing.T) {
	clearData(t)
	ctx := testCtx
	ownerID, orgID := mustUserAndOrg(t, ctx, "key-owner@test.invalid", "key-owner", "Key Quota Org")
	setEntitlementLimit(t, ctx, orgID, ownerID, business.EntitlementAPIKeys, 1)

	errs := runConcurrently(2, func(index int) error {
		_, err := testService.CreateAPIKey(ctx, ownerID, &gen.CreateAPIKeyRequest{
			OrganizationId: orgID,
			Name:           fmt.Sprintf("concurrent-key-%d", index),
			Environment:    gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_TEST,
		})
		return err
	})
	requireOneQuotaRejection(t, errs)

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		active, err := testStore.CountActiveAPIKeys(ctx, orgID)
		require.NoError(t, err)
		require.Equal(t, int64(1), active)
		return nil
	}))
}

func TestExpiredCardinalityResourcesReleaseCapacity(t *testing.T) {
	clearData(t)
	ctx := testCtx
	ownerID, orgID := mustUserAndOrg(t, ctx, "expiry-owner@test.invalid", "expiry-owner", "Expiry Org")
	setEntitlementLimit(t, ctx, orgID, ownerID, business.EntitlementAPIKeys, 1)
	setEntitlementLimit(t, ctx, orgID, ownerID, business.EntitlementSeats, 2)

	past := time.Now().Add(-time.Hour)
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := testStore.CreateAPIKey(ctx, &gen.APIKey{
			Id: business.NewIDString(), OrganizationId: orgID, UserId: ownerID,
			Name: "expired", Prefix: "expired-key", Environment: gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_TEST,
			ExpiresAt: timestamppb.New(past),
		}, "expired-key-hash"); err != nil {
			return err
		}
		return testStore.CreateInvitation(ctx, &business.Invitation{
			ID: business.NewIDString(), OrgID: orgID, InviterID: ownerID,
			Email: "retry@test.invalid", Role: "member", TokenHash: "expired-invitation-hash",
			Status: "pending", ExpiresAt: past,
		})
	}))

	_, err := testService.CreateAPIKey(ctx, ownerID, &gen.CreateAPIKeyRequest{
		OrganizationId: orgID, Name: "replacement", Environment: gen.APIKeyEnvironment_API_KEY_ENVIRONMENT_TEST,
	})
	require.NoError(t, err, "an expired key must not consume active-key capacity")

	_, err = testService.CreateInvitation(ctx, ownerID, &gen.CreateInvitationRequest{
		OrgId: orgID, Email: "retry@test.invalid", Role: gen.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.NoError(t, err, "an expired pending invitation must release its seat and uniqueness reservation")

	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		activeKeys, err := testStore.CountActiveAPIKeys(ctx, orgID)
		require.NoError(t, err)
		require.Equal(t, int64(1), activeKeys)
		pending, err := testStore.CountPendingInvitations(ctx, orgID)
		require.NoError(t, err)
		require.Equal(t, int32(1), pending)
		invitations, err := testStore.ListInvitations(ctx, orgID, "")
		require.NoError(t, err)
		require.Len(t, invitations, 2)
		statuses := map[string]int{}
		for _, invitation := range invitations {
			statuses[invitation.Status]++
		}
		require.Equal(t, 1, statuses["expired"])
		require.Equal(t, 1, statuses["pending"])
		return nil
	}))
}

func setEntitlementLimit(t *testing.T, ctx context.Context, orgID, actorID, feature string, limit int64) {
	t.Helper()
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		value := limit
		return testStore.CreateEntitlementOverride(ctx, &business.EntitlementOverride{
			ID: business.NewIDString(), OrgID: orgID, Feature: feature,
			LimitValue: &value, Reason: "cardinality quota test", CreatedBy: actorID,
		})
	}))
}

func runConcurrently(count int, operation func(index int) error) []error {
	start := make(chan struct{})
	errs := make([]error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			errs[index] = operation(index)
		}(index)
	}
	close(start)
	wait.Wait()
	return errs
}

func requireOneQuotaRejection(t *testing.T, errs []error) {
	t.Helper()
	succeeded := 0
	rejected := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, business.ErrEntitlementQuotaExceeded):
			rejected++
		default:
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, rejected)
}
