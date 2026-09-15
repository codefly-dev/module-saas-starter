package business

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/eventcatalog"
	"accounts/pkg/events"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	followTenant    = "22222222-2222-2222-2222-222222222222"
	followPrincipal = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	// scope.granted is in the composed catalog and tenant-visible, so it reaches
	// the guard rather than being refused earlier as uncatalogued.
	followableType = "scope.granted"
	followedNoun   = "scope.boundary"
)

// followTxStore is the whole Store surface this path touches: ModulePublishEvent
// runs its write inside WithOrgTx and nothing else is reached.
type followTxStore struct {
	Store
}

func (followTxStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

// installFollowable points the exact-target guard at a declaration. No composed
// contribution declares a followable resource yet, so without this the guard's
// failing path is unreachable — it could be inverted, or deleted outright, with
// every test in the tree still green, which is exactly what these tests exist to
// prevent.
func installFollowable(t *testing.T) {
	t.Helper()
	original := followableLookup
	t.Cleanup(func() { followableLookup = original })
	followableLookup = func(candidate string) (eventcatalog.FollowableResource, bool) {
		if candidate != followableType {
			return eventcatalog.FollowableResource{}, false
		}
		return eventcatalog.FollowableResource{
			ResourceType: followedNoun,
			Namespace:    "scope",
			Events:       []string{followableType},
		}, true
	}
}

func followEnvelope(eventType, subject string) *events.EventEnvelope {
	return &events.EventEnvelope{
		Id:           uuid.NewString(),
		Type:         eventType,
		Source:       "saas.accounts",
		Specversion:  "1.0",
		Subject:      subject,
		PartitionKey: followTenant,
		Data:         []byte(`{}`),
	}
}

func newFollowService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(followTxStore{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetModulePrincipals(ModulePrincipalRegistry{
		followPrincipal: {Namespaces: []string{"scope"}},
	})
	svc.SetModuleEventTransport(events.NewFakeTransport(nil, time.Second))
	return svc
}

// TestModulePublishEventRefusesFollowableWithoutSubject covers the exact-target
// rule. A declared followable event with no subject targets nothing: it would
// match no follower and deliver nothing, which is indistinguishable at every
// later layer from a resource nobody follows, so it is refused at publish.
func TestModulePublishEventRefusesFollowableWithoutSubject(t *testing.T) {
	installFollowable(t)
	svc := newFollowService(t)
	caller := ModuleCaller{PrincipalID: followPrincipal, BoundOrg: followTenant}

	_, err := svc.ModulePublishEvent(context.Background(), caller, followTenant, followEnvelope(followableType, ""))
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a declared followable event with no subject must be InvalidArgument, got %v", err)
	}

	// Carrying the resource id, the same event publishes normally.
	if _, err := svc.ModulePublishEvent(context.Background(), caller, followTenant, followEnvelope(followableType, "boundary-7")); err != nil {
		t.Fatalf("a followable event carrying its subject must publish: %v", err)
	}

	// A type nothing declares followable is unaffected: the subject is required by
	// the declaration, never by publishing in general.
	if _, err := svc.ModulePublishEvent(context.Background(), caller, followTenant, followEnvelope("scope.revoked", "")); err != nil {
		t.Fatalf("an undeclared type must not require a subject: %v", err)
	}
}
