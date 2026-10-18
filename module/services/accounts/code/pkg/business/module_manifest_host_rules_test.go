package business

import (
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// The host's own rules about a manifest: one workload runs as one manifest, a
// principal and a publisher stay with their ownership domain, and an endpoint
// is its workload's in-cluster Service.

// TWO NEW CLAIMANTS OF ONE WORKLOAD: the alias check judges what is applied,
// so both pass it; the first in delivery order keeps the workload and the
// second is withheld, naming the binding that holds it.
func TestASecondManifestForAHeldWorkloadIsWithheld(t *testing.T) {
	first := fixtureManifest(t, "reports", 1)
	second := fixtureManifest(t, "reports", 1)
	second.Binding = "acme.test.second-reports"

	admission := admit(t, nil, first, second)
	require.Len(t, admission.Accepted, 1)
	require.Equal(t, first.Binding, admission.Accepted[0].Document.Binding)
	require.Contains(t, admission.Withheld[second.Binding], `held by binding "acme.test.reports"`)

	err := testModuleNamespaceRule.checkManifestAgainstHost(second, []*solutionhost.ModuleManifest{first})
	require.ErrorIs(t, err, ErrSolutionDeliveryConflict)
	require.ErrorContains(t, err, "workload reports/reports")

	// A withdrawal holds nothing, and another account in the namespace is
	// another workload.
	require.NoError(t, testModuleNamespaceRule.checkManifestAgainstHost(second, []*solutionhost.ModuleManifest{withdrawalOf(t, first, 2)}))
	other := fixtureManifest(t, "reports", 1)
	other.Binding = "acme.test.other-reports"
	other.Workloads = []solutionhost.Workload{{Service: "worker", Container: "worker"}}
	require.NoError(t, testModuleNamespaceRule.checkManifestAgainstHost(other, []*solutionhost.ModuleManifest{first}))
}

// A FOREIGN DOMAIN CANNOT CLAIM A HELD PRINCIPAL OR PUBLISHER: a signer pinned
// for one domain speaks for that domain's bindings, so a manifest of another
// domain asking for a principal, or deploying a publisher, that one of them
// holds is refused, naming the holder. The same domain is not foreign.
func TestAForeignDomainCannotClaimAHeldPrincipalOrPublisher(t *testing.T) {
	held := fixtureManifest(t, "reports", 1)
	held.Authority = &solutionhost.Authority{Principal: "reports"}

	foreign := func() *solutionhost.ModuleManifest {
		claim := fixtureManifest(t, "ledger", 1)
		claim.Binding, claim.Host.Domain = "globex.test.ledger", "globex"
		claim.Release.Publisher = "globex"
		return claim
	}

	principal := foreign()
	principal.Authority = &solutionhost.Authority{Principal: "reports"}
	err := testModuleNamespaceRule.checkManifestAgainstHost(principal, []*solutionhost.ModuleManifest{held})
	require.ErrorIs(t, err, ErrSolutionDeliveryDomainNotGranted)
	require.ErrorContains(t, err, `principal "reports" is held by binding "acme.test.reports" of domain "acme"`)

	publisher := foreign()
	publisher.Release.Publisher = "acme"
	err = testModuleNamespaceRule.checkManifestAgainstHost(publisher, []*solutionhost.ModuleManifest{held})
	require.ErrorIs(t, err, ErrSolutionDeliveryDomainNotGranted)
	require.ErrorContains(t, err, `release publisher "acme" is held by binding "acme.test.reports"`)

	require.NoError(t, testModuleNamespaceRule.checkManifestAgainstHost(foreign(), []*solutionhost.ModuleManifest{held}),
		"a principal and a publisher of its own are the foreign domain's to claim")
	sameDomain := fixtureManifest(t, "ledger", 1)
	sameDomain.Authority = &solutionhost.Authority{Principal: "reports"}
	require.NoError(t, testModuleNamespaceRule.checkManifestAgainstHost(sameDomain, []*solutionhost.ModuleManifest{held}))
	require.NoError(t, testModuleNamespaceRule.checkManifestAgainstHost(principal, []*solutionhost.ModuleManifest{withdrawalOf(t, held, 2)}),
		"a withdrawn binding holds no principal")
}

// AN ENDPOINT IS ITS WORKLOAD'S IN-CLUSTER SERVICE, by the cell's rule — not
// the metadata server, not another module's namespace, not a free host — and
// a native run's endpoints are its own processes on the loopback.
func TestAnEndpointIsItsWorkloadsInClusterService(t *testing.T) {
	cell, err := NewModuleNamespaceRule("platform-acme-test-saas", "saas")
	require.NoError(t, err)
	check := func(rule ModuleNamespaceRule, address string) error {
		return rule.checkEndpointAddress("reports", solutionhost.Endpoint{Name: "rest", Service: "api", API: "rest", Address: address})
	}
	for _, address := range []string{
		"http://api.platform-acme-test-reports.svc.cluster.local:8080",
		"https://api.platform-acme-test-reports.svc.cluster.local:8443",
		"http://api.platform-acme-test-reports.svc.cluster.local:8080/",
	} {
		require.NoError(t, check(cell, address), address)
	}
	for _, address := range []string{
		"http://169.254.169.254:80",
		"http://169.254.169.254/latest/meta-data",
		"http://api.platform-acme-test-billing.svc.cluster.local:8080",
		"http://api.reports.svc.cluster.local:8080",
		"http://worker.platform-acme-test-reports.svc.cluster.local:8080",
		"http://api.platform-acme-test-reports.svc.cluster.local",
		"http://api.platform-acme-test-reports.svc.cluster.local:8080/v1",
		"http://api.platform-acme-test-reports.svc.cluster.local:8080?x=1",
		"http://user@api.platform-acme-test-reports.svc.cluster.local:8080",
		"ftp://api.platform-acme-test-reports.svc.cluster.local:8080",
		"api.platform-acme-test-reports.svc.cluster.local:8080",
		"http://127.0.0.1:8080",
	} {
		require.Error(t, check(cell, address), address)
	}

	native := NativeModuleNamespaceRule()
	for _, address := range []string{"http://127.0.0.1:9000", "http://localhost:9000", "http://[::1]:9000"} {
		require.NoError(t, check(native, address), address)
	}
	for _, address := range []string{"http://169.254.169.254:80", "http://api.reports.svc.cluster.local:8080", "http://10.0.0.7:9000"} {
		require.Error(t, check(native, address), address)
	}

	manifest := fixtureManifest(t, "reports", 1)
	manifest.Endpoints = []solutionhost.Endpoint{{Name: "rest", Service: "reports", API: "rest", Address: "http://169.254.169.254:80"}}
	err = testModuleNamespaceRule.checkManifestAgainstHost(manifest, nil)
	require.ErrorIs(t, err, ErrSolutionDeliveryRefused)
	require.ErrorContains(t, err, "169.254.169.254")
}
