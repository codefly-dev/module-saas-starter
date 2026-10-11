//go:build pure

package adapters

import (
	"context"
	"errors"
	"testing"

	"accounts/pkg/business"

	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

// The host as core's SealSource. Every method is a LIVE read, and these pin the
// translations where getting one wrong would make a comparison tautological or
// silently permissive.

type sealAuthority struct {
	live *business.LiveModuleAuthority
	err  error
	// asked records the arguments, because the scope a seal is compared in must
	// come from the source's construction and never from the capability.
	askedPrincipal, askedOrg, askedInstallation string
}

func (f *sealAuthority) LiveModuleAuthority(
	_ context.Context, principalID, orgID, installationID string,
) (*business.LiveModuleAuthority, error) {
	f.askedPrincipal, f.askedOrg, f.askedInstallation = principalID, orgID, installationID
	if f.err != nil {
		return nil, f.err
	}
	return f.live, nil
}

type sealBuilds struct {
	digest      business.ApprovedDigest
	incarnation uint64
	err         error
}

func (f *sealBuilds) ApprovedBuild(
	context.Context, string,
) (business.ApprovedDigest, uint64, error) {
	if f.err != nil {
		return "", 0, f.err
	}
	return f.digest, f.incarnation, nil
}

type sealBindings struct {
	binding *business.ModuleOperationBinding
	err     error
	asked   []string
}

func (f *sealBindings) OperationBindingByID(
	_ context.Context, _, bindingID string,
) (*business.ModuleOperationBinding, error) {
	f.asked = append(f.asked, bindingID)
	if f.err != nil {
		return nil, f.err
	}
	return f.binding, nil
}

// Seal answers the live installation, scoped to the tenant the SOURCE was built
// for — not one read off the capability being verified.
//
// Core's SealSource takes no organisation, so if the scope came from the
// capability, the capability would choose the scope its own seal is compared in.
func TestSealIsScopedToTheSourcesTenantNotTheCapability(t *testing.T) {
	authority := &sealAuthority{live: &business.LiveModuleAuthority{
		InstallationID: "install-1", InstallationRevision: 9, ProducerEpoch: 4,
	}}
	source := NewWorkContextSealSource("org-1", authority, nil, nil)

	seal, err := source.Seal(context.Background(), "principal-1", "install-1")
	require.NoError(t, err)
	require.Equal(t, "install-1", seal.InstallationID)
	require.Equal(t, uint64(9), seal.InstallationRevision)
	require.Equal(t, "org-1", authority.askedOrg, "the tenant is the source's, bound at construction")
	require.Equal(t, "install-1", authority.askedInstallation)
}

// A revoked installation is an ERROR, not a zero Seal.
//
// Zero is a real revision a fresh organisation has, so returning one would
// compare equal to a capability sealed before any revision existed — the
// absence-is-not-zero rule, in core's direction.
func TestARevokedInstallationIsAnErrorNotAZeroSeal(t *testing.T) {
	source := NewWorkContextSealSource("org-1",
		&sealAuthority{err: business.ErrModuleInstallationInactive}, nil, nil)

	seal, err := source.Seal(context.Background(), "principal-1", "install-gone")
	require.Error(t, err)
	require.Zero(t, seal.InstallationRevision)
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)
}

// PrincipalEpoch reads with NO installation, because the epoch belongs to the
// principal in the organisation.
//
// Passing one would make a principal's epoch unanswerable for a capability that
// names no installation — which is every capability until the mint seals one.
func TestPrincipalEpochReadsWithoutAnInstallation(t *testing.T) {
	authority := &sealAuthority{live: &business.LiveModuleAuthority{ProducerEpoch: 7}}
	source := NewWorkContextSealSource("org-1", authority, nil, nil)

	epoch, err := source.PrincipalEpoch(context.Background(), "principal-1")
	require.NoError(t, err)
	require.Equal(t, uint64(7), epoch)
	require.Empty(t, authority.askedInstallation,
		"an epoch is a property of the principal, not of any installation")
}

// "Bears no approved build" is translated to CORE's sentinel, because core's
// mint and verify branch on it.
//
// Returning the host's own error would read to core as a failure to ANSWER
// rather than as "bears none", and those have opposite consequences: one is an
// outage, the other is a capability that legitimately carries no execution.
func TestBearingNoApprovedBuildBecomesCoresSentinel(t *testing.T) {
	source := NewWorkContextSealSource("org-1", nil,
		&sealBuilds{err: business.ErrNoApprovedBuild}, nil)

	_, _, err := source.ApprovedBuild(context.Background(), "principal-1")
	require.ErrorIs(t, err, workcontext.ErrNoApprovedBuild)
}

// An UNKNOWN principal is NOT translated to "bears none".
//
// Collapsing them would admit a capability for an identity this host has never
// heard of as merely unbound — the dangerous direction, and the one the
// three-state distinction exists for.
func TestAnUnknownPrincipalIsNotReportedAsBearingNone(t *testing.T) {
	source := NewWorkContextSealSource("org-1", nil,
		&sealBuilds{err: business.ErrUnknownExecutionPrincipal}, nil)

	_, _, err := source.ApprovedBuild(context.Background(), "stranger")
	require.Error(t, err)
	require.NotErrorIs(t, err, workcontext.ErrNoApprovedBuild,
		"an unknown principal must not read to core as a principal bearing no execution")
}

// An approved build answers with its digest and incarnation, both from the
// authority rather than from anything the caller presented.
func TestAnApprovedBuildAnswersDigestAndIncarnation(t *testing.T) {
	source := NewWorkContextSealSource("org-1", nil,
		&sealBuilds{digest: business.ApprovedDigest("sha256:abc"), incarnation: 5}, nil)

	digest, incarnation, err := source.ApprovedBuild(context.Background(), "principal-1")
	require.NoError(t, err)
	require.Equal(t, "sha256:abc", digest)
	require.Equal(t, uint64(5), incarnation)
}

// A binding is resolved by EXACT id, and a missing one is core's ErrNoBinding
// rather than a zero binding.
//
// A zero OperationBinding would have revision 0, which a capability sealed at
// revision 0 would match — so "no such binding" must not be expressible as a
// successful answer.
func TestAMissingBindingIsCoresSentinelNotAZeroBinding(t *testing.T) {
	bindings := &sealBindings{binding: nil}
	source := NewWorkContextSealSource("org-1", nil, nil, bindings)

	_, err := source.OperationBinding(context.Background(), "binding-gone")
	require.ErrorIs(t, err, workcontext.ErrNoBinding)
	require.Equal(t, []string{"binding-gone"}, bindings.asked,
		"exactly the named binding is looked up — never a search")
}

// A present binding answers with the record's own revision.
func TestAPresentBindingAnswersItsRevision(t *testing.T) {
	source := NewWorkContextSealSource("org-1", nil, nil,
		&sealBindings{binding: &business.ModuleOperationBinding{BindingID: "binding-1", Revision: 3}})

	binding, err := source.OperationBinding(context.Background(), "binding-1")
	require.NoError(t, err)
	require.Equal(t, "binding-1", binding.ID)
	require.Equal(t, uint64(3), binding.Revision)
}

// An unwired source REFUSES every question rather than answering a zero.
//
// Core has no seal-less mode, so a source that answered zeros would make every
// comparison pass while nothing was read — the exact shape of a check that does
// not run.
func TestAnUnwiredSealSourceRefusesEveryQuestion(t *testing.T) {
	source := NewWorkContextSealSource("org-1", nil, nil, nil)

	_, err := source.Seal(context.Background(), "p", "i")
	require.Error(t, err)
	_, err = source.PrincipalEpoch(context.Background(), "p")
	require.Error(t, err)
	_, _, err = source.ApprovedBuild(context.Background(), "p")
	require.Error(t, err)
	_, err = source.OperationBinding(context.Background(), "b")
	require.Error(t, err)
}

// A failing live read propagates as a failure, never as an answer.
func TestAFailingLiveReadIsNotAnAnswer(t *testing.T) {
	source := NewWorkContextSealSource("org-1",
		&sealAuthority{err: errors.New("connection reset")}, nil, nil)

	_, err := source.Seal(context.Background(), "p", "i")
	require.Error(t, err)
	_, err = source.PrincipalEpoch(context.Background(), "p")
	require.Error(t, err)
}
