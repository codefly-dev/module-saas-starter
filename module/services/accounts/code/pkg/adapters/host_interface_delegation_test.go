package adapters

import (
	"context"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"

	accountsauth "accounts/pkg/auth"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
)

// What a delegation leaves of what the owner holds is the meet, kind by kind:
// never an action or a resource either side lacks, and a kind only one names
// is gone.
func TestADelegationCutsWhatTheOwnerHoldsKindByKind(t *testing.T) {
	held := []*basev0.WorkScopeV1{
		hopScope("example-records", []string{"invoke", "read"}),
		hopScope("tool.items", []string{"read"}),
	}
	delegated := []*basev0.WorkScopeV1{
		hopScope("example-records", []string{"read", "write"}, "a"),
		hopScope("modelservice.profiles", []string{"invoke"}),
	}
	requireScopes(t, []*basev0.WorkScopeV1{hopScope("example-records", []string{"read"}, "a")}, intersectScopes(held, delegated))
	if got := intersectScopes(held, []*basev0.WorkScopeV1{hopScope("example-records", []string{"write"})}); len(got) != 0 {
		t.Fatalf("a delegation of what the owner does not hold leaves %v", got)
	}
}

// A delegation is made and revoked by a person in their own right: the
// credentials every other person mint holds back are refused by name before
// anything is read or recorded.
func TestADelegationRefusesACredentialThatIsNotThePersonsOwn(t *testing.T) {
	subject := uuid.New()
	person := accountsauth.RequestIdentity{RealActor: subject, EffectiveSubject: subject, OrgID: uuid.New(), SessionID: uuid.New()}
	signedIn := func() context.Context {
		return withCredentialKind(accountsauth.WithVerifiedRequestIdentity(context.Background(), person), credentialKindSession)
	}
	acting := person
	acting.Delegation = &accountsauth.Actor{Subject: "a-service"}
	for name, tc := range map[string]struct {
		ctx    context.Context
		reason string
	}{
		"a resource-confined credential":         {accountsauth.WithVerifiedResourceConfinement(signedIn(), "https://acme.test/solutions/pages/mcp"), "resource-confined"},
		"an identity acting on someone's behalf": {accountsauth.WithVerifiedActor(signedIn(), &accountsauth.Actor{Subject: "a-service"}), "X-Act"},
		"a delegated identity":                   {withCredentialKind(accountsauth.WithVerifiedRequestIdentity(context.Background(), acting), credentialKindSession), "X-Act"},
		"an API key":                             {withCredentialKind(accountsauth.WithVerifiedRequestIdentity(context.Background(), person), credentialKindAPIKey), "interactive session"},
		"no bearer":                              {context.Background(), "carries no bearer"},
	} {
		t.Run(name, func(t *testing.T) {
			code := codes.PermissionDenied
			if tc.reason == "carries no bearer" {
				code = codes.Unauthenticated
			}
			_, err := (&HostAuthorityServer{}).Delegate(tc.ctx, &authorityv1.DelegateRequest{Module: "documents",
				Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "example-records", Actions: []string{"read"}}}})
			requireRefused(t, err, code, tc.reason)
			_, err = (&HostAuthorityServer{}).RevokeDelegation(tc.ctx, &authorityv1.RevokeDelegationRequest{DelegationId: uuid.NewString()})
			requireRefused(t, err, code, tc.reason)
		})
	}
}
