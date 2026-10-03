//go:build pure

package business

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Enforcement at use, and every way a narrowing could fail to reach a
// capability.
//
// The coverage gate in module/tools proves every path is ROUTED through the
// enforcing wrapper. These prove the wrapper DECIDES correctly — the two are
// different claims, and a gate alone would hold 25 paths to a check that did
// nothing.

type fakeModuleAuthority struct {
	live *LiveModuleAuthority
	err  error
	// calls counts the live reads, so a test can prove the check actually ran
	// rather than inferring it from a verdict that might have other causes.
	calls int
}

func (f *fakeModuleAuthority) LiveModuleAuthority(
	context.Context, string, string,
) (*LiveModuleAuthority, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.live, nil
}

type fakeOperationBindings struct {
	byID map[string]*ModuleOperationBinding
	err  error
	// asked records every binding id looked up, which is how a SEARCH is
	// distinguished from an exact lookup: a search would ask for none of them by
	// id and instead enumerate.
	asked []string
}

func (f *fakeOperationBindings) OperationBindingByID(
	_ context.Context, _, bindingID string,
) (*ModuleOperationBinding, error) {
	f.asked = append(f.asked, bindingID)
	if f.err != nil {
		return nil, f.err
	}
	return f.byID[bindingID], nil
}

// enforcingService wires a service whose registry declares one module principal.
func enforcingService(authority ModuleAuthorityStore, bindings ModuleOperationBindingStore) *Service {
	service := &Service{store: noopControlPlaneStore{}}
	service.SetModulePrincipals(ModulePrincipalRegistry{
		"principal-1": {Prefix: "crm", Tenant: "org-1", Queues: []string{"a"}},
	})
	service.SetModuleAuthorityReads(authority, bindings)
	return service
}

func caller() ModuleCaller {
	return ModuleCaller{PrincipalID: "principal-1", BoundOrg: "org-1"}
}

func liveAt(revision, epoch uint64) *fakeModuleAuthority {
	return &fakeModuleAuthority{live: &LiveModuleAuthority{
		InstallationID:       "install-1",
		InstallationRevision: revision,
		ProducerEpoch:        epoch,
	}}
}

// ---------------------------------------------------------------------------
// The live read happens at all
// ---------------------------------------------------------------------------

// The declared ceiling is not enough on its own: the live installation is read
// on every decision.
func TestCapabilityReReadsLiveAuthority(t *testing.T) {
	authority := liveAt(5, 2)
	service := enforcingService(authority, nil)

	got, err := service.AuthorizeModuleCapability(context.Background(), caller())
	require.NoError(t, err)
	require.Equal(t, 1, authority.calls, "the live installation must be read, not assumed")
	require.Equal(t, "install-1", got.InstallationID)
	require.Equal(t, uint64(5), got.InstallationRevision)
	require.Equal(t, uint64(2), got.ProducerEpoch)
	require.Equal(t, []string{"a"}, got.Grant.Queues, "the declared ceiling still bounds it")
}

// An uninstalled solution's capability is refused, and the refusal is a VERDICT.
func TestCapabilityIsRefusedWhenTheInstallationIsInactive(t *testing.T) {
	service := enforcingService(&fakeModuleAuthority{err: ErrModuleInstallationInactive}, nil)

	_, err := service.AuthorizeModuleCapability(context.Background(), caller())
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// A host that CANNOT re-read answers Unavailable — neither a denial nor an
// allow.
//
// Treating it as a denial would make a database blip look like mass revocation;
// treating it as an allow would be the check not running. The distinction is the
// same one the Kubernetes client makes, for the same reason.
func TestCapabilityIsUndecidableWhenTheLiveReadFails(t *testing.T) {
	service := enforcingService(&fakeModuleAuthority{err: errors.New("connection reset")}, nil)

	_, err := service.AuthorizeModuleCapability(context.Background(), caller())
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotEqual(t, codes.PermissionDenied, status.Code(err),
		"an unreadable authority is not a revoked one")
}

// A host with no live read at all refuses every capability rather than falling
// back to the declared ceiling — which is the state enforcement at use exists to
// end.
func TestCapabilityIsRefusedWithoutALiveReadWired(t *testing.T) {
	service := &Service{store: noopControlPlaneStore{}}
	service.SetModulePrincipals(ModulePrincipalRegistry{
		"principal-1": {Prefix: "crm", Tenant: "org-1"},
	})

	_, err := service.AuthorizeModuleCapability(context.Background(), caller())
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// An unknown principal is refused WITHOUT a live read: the registry is the
// allowlist, and an unauthenticated caller must not be able to make this host do
// database work.
func TestUnknownPrincipalIsRefusedBeforeTheLiveRead(t *testing.T) {
	authority := liveAt(1, 1)
	service := enforcingService(authority, nil)

	_, err := service.AuthorizeModuleCapability(context.Background(),
		ModuleCaller{PrincipalID: "stranger", BoundOrg: "org-1"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Zero(t, authority.calls, "an unknown principal must not cost a database round trip")
}

// ---------------------------------------------------------------------------
// min(sealed, live): the intersection, and the absence trap
// ---------------------------------------------------------------------------

// A credential sealed at an older installation revision is refused: the
// authority moved after it was minted.
//
// Without the revision, an uninstall-and-reinstall produces a new installation
// that the old credential would satisfy.
func TestSealedStaleInstallationRevisionIsRefused(t *testing.T) {
	service := enforcingService(liveAt(6, 2), nil)

	sealedRevision := uint64(5)
	subject := caller()
	subject.Sealed = &SealedModuleAuthority{InstallationRevision: &sealedRevision}

	_, err := service.AuthorizeModuleCapability(context.Background(), subject)
	require.ErrorIs(t, err, ErrModuleAuthorityStale)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// A credential sealed at an older producer epoch is refused.
//
// This is the term that stops a narrowing undoing itself: without it, narrowing
// revokes only the credential being held and the replacement is reminted with
// the old authority.
func TestSealedStaleProducerEpochIsRefused(t *testing.T) {
	service := enforcingService(liveAt(5, 3), nil)

	sealedEpoch := uint64(2)
	subject := caller()
	subject.Sealed = &SealedModuleAuthority{ProducerEpoch: &sealedEpoch}

	_, err := service.AuthorizeModuleCapability(context.Background(), subject)
	require.ErrorIs(t, err, ErrModuleAuthorityStale)
}

// A credential sealed for a DIFFERENT installation is refused even at a matching
// revision — the installation id is the identity, and revisions are per
// organisation.
func TestSealedForAnotherInstallationIsRefused(t *testing.T) {
	service := enforcingService(liveAt(5, 2), nil)

	other := "install-elsewhere"
	subject := caller()
	subject.Sealed = &SealedModuleAuthority{InstallationID: &other}

	_, err := service.AuthorizeModuleCapability(context.Background(), subject)
	require.ErrorIs(t, err, ErrModuleAuthorityStale)
}

// Matching seals pass. Without this the refusal tests could pass against a
// wrapper that refuses everything sealed.
func TestMatchingSealsPass(t *testing.T) {
	service := enforcingService(liveAt(5, 2), nil)

	id, revision, epoch := "install-1", uint64(5), uint64(2)
	subject := caller()
	subject.Sealed = &SealedModuleAuthority{
		InstallationID: &id, InstallationRevision: &revision, ProducerEpoch: &epoch,
	}

	got, err := service.AuthorizeModuleCapability(context.Background(), subject)
	require.NoError(t, err)
	require.Equal(t, "install-1", got.InstallationID)
}

// THE ABSENCE TRAP, and the single most important test in this file.
//
// A credential that carries NO claim about a term must not be compared against
// zero. "Treat missing as zero" is the shorter implementation and it silently
// admits every unsealed credential while looking like a check — and every
// credential is unsealed until the Work Context cutover, so it would admit all
// of them.
//
// A live revision of ZERO is the case that exposes it: with pointers, an absent
// seal is skipped and a zero seal is compared. Flatten them to values and the
// two become indistinguishable.
func TestAbsentSealIsNotComparedAgainstZero(t *testing.T) {
	// Live authority at revision 0 and epoch 0 — real values a fresh
	// organisation genuinely has.
	service := enforcingService(liveAt(0, 0), nil)

	// A credential claiming nothing passes: there is nothing to intersect.
	unsealed := caller()
	_, err := service.AuthorizeModuleCapability(context.Background(), unsealed)
	require.NoError(t, err, "a credential that claims nothing is bounded by the live read alone")

	// A credential explicitly claiming revision 0 also passes, because 0 is a
	// real revision and it matches.
	zero := uint64(0)
	claimsZero := caller()
	claimsZero.Sealed = &SealedModuleAuthority{InstallationRevision: &zero}
	_, err = service.AuthorizeModuleCapability(context.Background(), claimsZero)
	require.NoError(t, err)

	// And one claiming revision 0 against a LIVE revision of 1 is refused — which
	// is what distinguishes "claims zero" from "claims nothing".
	moved := enforcingService(liveAt(1, 0), nil)
	_, err = moved.AuthorizeModuleCapability(context.Background(), claimsZero)
	require.ErrorIs(t, err, ErrModuleAuthorityStale,
		"a credential claiming revision 0 is making a claim, and it is now wrong")

	// While the one claiming nothing still passes against the same live state.
	_, err = moved.AuthorizeModuleCapability(context.Background(), unsealed)
	require.NoError(t, err, "absence is not a claim about zero")
}

// A PARTIALLY sealed credential is the case that actually catches "treat missing
// as zero", and the case above does not.
//
// A wholly-nil seal short-circuits before any field is compared, so a mutation
// that flattens a missing FIELD to zero slips past it — which is exactly what
// happened: the first version of the absence test passed against that mutation,
// and only this case fails.
//
// It is also the realistic shape during the Work Context cutover: a credential
// that carries the installation id because the mint can answer it, and no
// revision because the seal does not yet have the field. Flatten the missing
// revision to zero, compare it against a live revision of 1, and every such
// credential is refused — the check inverts from admitting too much to refusing
// everything, and both are wrong.
func TestPartiallySealedCredentialSkipsOnlyTheTermsItOmits(t *testing.T) {
	// Live at revision 1 and epoch 1, so a flattened zero would MISMATCH.
	service := enforcingService(liveAt(1, 1), nil)

	id := "install-1"
	partial := caller()
	partial.Sealed = &SealedModuleAuthority{
		// Claims the installation, and deliberately nothing else.
		InstallationID: &id,
	}

	got, err := service.AuthorizeModuleCapability(context.Background(), partial)
	require.NoError(t, err,
		"a credential claiming the installation but no revision must be bounded by the term it claims, "+
			"not refused for a term it is silent about")
	require.Equal(t, uint64(1), got.InstallationRevision)

	// And the term it DOES claim is still enforced.
	elsewhere := "install-elsewhere"
	wrong := caller()
	wrong.Sealed = &SealedModuleAuthority{InstallationID: &elsewhere}
	_, err = service.AuthorizeModuleCapability(context.Background(), wrong)
	require.ErrorIs(t, err, ErrModuleAuthorityStale)
}

// ---------------------------------------------------------------------------
// The exact binding lookup, which is NOT a search
// ---------------------------------------------------------------------------

// The binding is resolved by EXACT id, and the id asked for is the one the
// credential sealed.
//
// `operationScopesSubset` searched the principal's bindings for one whose scopes
// contained the presented set — so a credential naming a narrow binding was
// satisfied by any WIDER binding the principal also held, and narrowing one
// binding achieved nothing while a broader one survived.
func TestOperationBindingIsResolvedByExactID(t *testing.T) {
	bindings := &fakeOperationBindings{byID: map[string]*ModuleOperationBinding{
		"binding-narrow": {BindingID: "binding-narrow", Revision: 3, Scopes: []string{"read"}},
		"binding-wide":   {BindingID: "binding-wide", Revision: 1, Scopes: []string{"read", "write"}},
	}}
	service := enforcingService(liveAt(5, 2), bindings)

	sealedID, sealedRevision := "binding-narrow", uint64(3)
	subject := caller()
	subject.Sealed = &SealedModuleAuthority{BindingID: &sealedID, BindingRevision: &sealedRevision}

	authority, err := service.AuthorizeModuleCapability(context.Background(), subject)
	require.NoError(t, err)

	binding, err := service.ExactOperationBinding(context.Background(), authority, subject.Sealed)
	require.NoError(t, err)
	require.Equal(t, "binding-narrow", binding.BindingID)
	require.Equal(t, []string{"binding-narrow"}, bindings.asked,
		"exactly the sealed binding is looked up — the wider one is never consulted")
}

// A capability naming NO binding is refused rather than falling back to a
// search. The fallback is the whole defect: an optional exact lookup is a search
// with extra steps.
func TestOperationBindingWithoutASealedIDIsRefused(t *testing.T) {
	bindings := &fakeOperationBindings{byID: map[string]*ModuleOperationBinding{
		"binding-wide": {BindingID: "binding-wide", Scopes: []string{"read", "write"}},
	}}
	service := enforcingService(liveAt(5, 2), bindings)

	authority, err := service.AuthorizeModuleCapability(context.Background(), caller())
	require.NoError(t, err)

	_, err = service.ExactOperationBinding(context.Background(), authority, nil)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Empty(t, bindings.asked, "no binding may be consulted when none was named")
}

// A binding the principal does not hold is refused, not resolved to a similar
// one.
func TestOperationBindingNotHeldIsRefused(t *testing.T) {
	bindings := &fakeOperationBindings{byID: map[string]*ModuleOperationBinding{}}
	service := enforcingService(liveAt(5, 2), bindings)

	sealedID := "binding-elsewhere"
	subject := caller()
	subject.Sealed = &SealedModuleAuthority{BindingID: &sealedID}
	authority, err := service.AuthorizeModuleCapability(context.Background(), subject)
	require.NoError(t, err)

	_, err = service.ExactOperationBinding(context.Background(), authority, subject.Sealed)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// A binding whose revision has moved is refused: the binding is the same one,
// and what it authorises has changed.
func TestOperationBindingAtAStaleRevisionIsRefused(t *testing.T) {
	bindings := &fakeOperationBindings{byID: map[string]*ModuleOperationBinding{
		"binding-1": {BindingID: "binding-1", Revision: 4},
	}}
	service := enforcingService(liveAt(5, 2), bindings)

	sealedID, sealedRevision := "binding-1", uint64(3)
	subject := caller()
	subject.Sealed = &SealedModuleAuthority{BindingID: &sealedID, BindingRevision: &sealedRevision}
	authority, err := service.AuthorizeModuleCapability(context.Background(), subject)
	require.NoError(t, err)

	_, err = service.ExactOperationBinding(context.Background(), authority, subject.Sealed)
	require.ErrorIs(t, err, ErrModuleAuthorityStale)
}

// ---------------------------------------------------------------------------
// Deferred work: re-checked when it RUNS, with a defined terminal outcome
// ---------------------------------------------------------------------------

// Deferred work is the case the live re-read alone cannot cover: the authority
// was current when the job was enqueued and the job runs later, so "at use" has
// to mean at the moment of use.
func TestDeferredWorkIsReCheckedWhenItRuns(t *testing.T) {
	service := enforcingService(liveAt(5, 2), nil)
	authority, err := service.AuthorizeModuleCapability(context.Background(), caller())
	require.NoError(t, err)
	stamp := authority.StampDeferredWork()

	// Still current: the work runs.
	_, err = service.AuthorizeDeferredWork(context.Background(), stamp)
	require.NoError(t, err)

	// The epoch moves — a narrowing landed between enqueue and run.
	moved := enforcingService(liveAt(5, 3), nil)
	_, err = moved.AuthorizeDeferredWork(context.Background(), stamp)
	require.ErrorIs(t, err, ErrDeferredWorkRevoked,
		"work enqueued under authority that has since been revoked must not run")
}

// Revoked deferred work is TERMINAL, not retryable: the authority will not come
// back by waiting, so retrying asks the same question forever. An unreadable
// authority IS retryable, because nothing was decided.
//
// Both in one test, because the distinction is the point and either alone could
// be satisfied by collapsing them.
func TestRevokedDeferredWorkIsTerminalWhileAnOutageIsRetryable(t *testing.T) {
	service := enforcingService(liveAt(5, 2), nil)
	authority, err := service.AuthorizeModuleCapability(context.Background(), caller())
	require.NoError(t, err)
	stamp := authority.StampDeferredWork()

	revoked := enforcingService(&fakeModuleAuthority{err: ErrModuleInstallationInactive}, nil)
	_, err = revoked.AuthorizeDeferredWork(context.Background(), stamp)
	require.ErrorIs(t, err, ErrDeferredWorkRevoked, "a revoked installation is terminal")

	unreadable := enforcingService(&fakeModuleAuthority{err: errors.New("connection reset")}, nil)
	_, err = unreadable.AuthorizeDeferredWork(context.Background(), stamp)
	require.NotErrorIs(t, err, ErrDeferredWorkRevoked,
		"an unreadable authority must stay retryable — nothing was decided")
	require.Equal(t, codes.Unavailable, status.Code(err))
}
