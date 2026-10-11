package adapters

import (
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"

	"github.com/stretchr/testify/require"
)

// moduleExecution is the cluster and registry the current test's service binds
// module mints against. A module proves which module it is with its pod's
// projected ServiceAccount token (#1056), so a test that mints asks this for a
// token rather than holding a secret.
var moduleExecution struct {
	cluster  *executiontest.Cluster
	registry business.ModulePrincipalRegistry
}

// bindModuleExecution wires svc to a fresh fake cluster and approves, for every
// declared principal, the build its pods run by default (named for its prefix).
func bindModuleExecution(t *testing.T, svc *business.Service, registry business.ModulePrincipalRegistry) *executiontest.Cluster {
	t.Helper()
	cluster := executiontest.New()
	svc.SetExecutionBinding(cluster, cluster)
	for _, grant := range registry {
		cluster.Approve(grant.Prefix, executiontest.Digest(grant.Prefix), 1)
	}
	previous := moduleExecution
	t.Cleanup(func() { moduleExecution = previous })
	moduleExecution.cluster, moduleExecution.registry = cluster, registry
	return cluster
}

// moduleToken schedules a pod for prefix's declared workload, running its
// approved build, and returns the token Kubernetes projects into it for
// accounts.
func moduleToken(t *testing.T, prefix string) string {
	t.Helper()
	require.NotNil(t, moduleExecution.cluster, "no module execution is bound for this test")
	grant, declared := moduleExecution.registry[business.ModulePrincipalID(prefix)]
	require.True(t, declared, "%s is not a declared principal", prefix)
	return moduleExecution.cluster.Run(moduleWorkload(grant), executiontest.Image(prefix))
}

func moduleWorkload(grant business.ModulePrincipalGrant) executiontest.Workload {
	return executiontest.Workload{
		Namespace: grant.Workload.Namespace, ServiceAccount: grant.Workload.ServiceAccount,
		Container: grant.Workload.Container,
	}
}
