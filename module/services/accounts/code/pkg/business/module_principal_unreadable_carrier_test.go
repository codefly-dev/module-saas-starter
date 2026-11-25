//go:build !race_only

package business_test

import (
	"context"
	"errors"
	"testing"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"

	solutionhost "github.com/codefly-dev/authority/libraries/solutionhost/go"
	"github.com/stretchr/testify/require"
)

// unreadableInbox is an inbox one binding's carrier cannot be read from.
type unreadableInbox struct {
	withheldInbox
	unreadable string
	reads      *int
}

var errCarrierUnreadable = errors.New("the carrier row did not read")

func (i unreadableInbox) ReadDeliveredDocument(ctx context.Context, binding string, generation uint64) (*business.SolutionDeliveryRecord, error) {
	*i.reads++
	if binding == i.unreadable {
		return nil, errCarrierUnreadable
	}
	return i.withheldInbox.ReadDeliveredDocument(ctx, binding, generation)
}

// The pass reads every applied carrier from the inbox in one transaction. A
// carrier that does not read fails that transaction, and must still withhold
// only its own binding's authority: the module beside it keeps what its
// verified manifest grants.
func TestACarrierThatDoesNotReadWithholdsOnlyItsOwnBinding(t *testing.T) {
	host := newWithheldHost(t, withheldEnvelope(t, true))
	require.NoError(t, host.derive(t))

	const broken = "acme.test.unreadable"
	other := withheldManifest(t)
	other.Binding, other.Alias = broken, "unreadable"
	other.Release.Name = "unreadable"
	other.Authority.Principal = "unreadable"
	require.NoError(t, other.Validate())
	digest, err := other.Digest()
	require.NoError(t, err)
	canonical, err := solutionhost.Marshal(other)
	require.NoError(t, err)
	// The unreadable binding sorts first, so the read that fails is the one
	// the transaction starts with.
	host.store.applied = append([]*business.SolutionHostBindingRecord{{
		BindingID: broken,
		Applied: &business.SolutionHostBindingApplied{
			SolutionHostBindingGeneration: business.SolutionHostBindingGeneration{
				Generation: other.Generation, Digest: digest, Document: string(canonical),
			},
		},
	}}, host.store.applied...)

	good := withheldManifest(t)
	payload, err := solutionhost.SignedPayload(good)
	require.NoError(t, err)
	goodDigest, err := good.Digest()
	require.NoError(t, err)
	reads := 0
	host.svc.SetSolutionDelivery(
		deliverytest.Verifier(t, map[string][]string{withheldSigner: {withheldDomain}}),
		unreadableInbox{unreadable: broken, reads: &reads, withheldInbox: withheldInbox{record: &business.SolutionDeliveryRecord{
			Binding: good.Binding, Generation: good.Generation, ContentHash: goodDigest,
			Payload: payload, Envelope: deliverytest.Envelope(t, payload, withheldSigner),
		}}}, nil)

	err = host.derive(t)
	require.ErrorIs(t, err, business.ErrModulePrincipalOmitted, "the binding whose carrier did not read grants nothing")
	require.ErrorIs(t, err, errCarrierUnreadable, "and its refusal names the read that failed")
	require.Contains(t, err.Error(), broken)
	require.NotContains(t, err.Error(), `"`+withheldManifestBinding+`"`, "the binding beside it was refused with it")

	source, err := host.svc.AddGitHubSource(context.Background(), "actor-1", business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", CollectionLabel: "guides", AccessToken: "t",
	})
	require.NoError(t, err, "the module whose carrier verified lost its authority to another binding's failed read")
	active, err := host.svc.ListSourceDelegations(context.Background(), testOrg, source.ID, false)
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, withheldPrefix, active[0].ModulePrefix)
}
