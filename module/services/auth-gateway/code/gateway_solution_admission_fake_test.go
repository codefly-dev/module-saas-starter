package main

import (
	"context"
	"sort"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"
)

// installedEverythingRegistered is the entitlement authority the shared gateway
// harness runs with: it answers that the viewer's organization installed, and
// was granted, every solution the registry currently holds.
//
// It exists so the ROUTING tests stay about routing. A solution's availability
// and an organization's admission are three different layers (available,
// installed, exposed) and the proxy now consults all three, so a routing test
// that said nothing about admission would be asserting a 403. Answering from the
// registry keeps the fake faithful — it never admits a solution that is not
// there — while letting a test that is ABOUT admission substitute a narrower
// authority and get a real refusal.
type installedEverythingRegistered struct {
	registry *fakeSolutionRegistry
}

func (a *installedEverythingRegistered) List(
	_ context.Context, _ *accountsv1.ListSolutionEntitlementsRequest,
) (*accountsv1.ListSolutionEntitlementsResponse, error) {
	a.registry.mu.Lock()
	// The entitled set is keyed on the TARGET each record is declared under, not
	// on the alias: that is what the gateway compares, and a fake answering
	// aliases would make every admission test green against the alias join this
	// change exists to remove.
	targets := make([]string, 0, len(a.registry.records))
	for _, record := range a.registry.records {
		if record.GetTombstonedAt() != nil || record.GetDeclared().GetTargetId() == "" {
			continue
		}
		targets = append(targets, record.GetDeclared().GetTargetId())
	}
	a.registry.mu.Unlock()
	sort.Strings(targets)
	resp := &accountsv1.ListSolutionEntitlementsResponse{}
	for _, target := range targets {
		resp.Entitlements = append(resp.Entitlements, &accountsv1.SolutionEntitlement{
			TargetId:        target,
			InstallationId:  "install-" + target,
			RootScopeNodeId: "node-" + target,
			Healthy:         true,
		})
	}
	return resp, nil
}

// entitledTo is an authority that admits exactly the named TARGETS, and
// records what it was asked — which is the point of most admission tests: the
// organization and viewer the authority is asked about must be the ones
// ext_authz verified.
type entitledTo struct {
	ids   []string
	calls []*accountsv1.ListSolutionEntitlementsRequest
}

func (a *entitledTo) List(
	_ context.Context, req *accountsv1.ListSolutionEntitlementsRequest,
) (*accountsv1.ListSolutionEntitlementsResponse, error) {
	a.calls = append(a.calls, req)
	resp := &accountsv1.ListSolutionEntitlementsResponse{}
	for _, id := range a.ids {
		resp.Entitlements = append(resp.Entitlements, &accountsv1.SolutionEntitlement{
			TargetId:        id,
			InstallationId:  "install-" + id,
			RootScopeNodeId: "node-" + id,
			Healthy:         true,
		})
	}
	return resp, nil
}
