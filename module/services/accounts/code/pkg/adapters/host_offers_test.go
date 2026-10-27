package adapters

import (
	"testing"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	runnablev0 "github.com/codefly-dev/core/generated/go/codefly/runnable/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/runnable"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	offerRead  = "/tools.Solution/Read"
	offerWrite = "/tools.Solution/Write"
)

func offerScope(kind string, actions ...string) *basev0.WorkScopeV1 {
	return &basev0.WorkScopeV1{ResourceKind: kind, Actions: actions}
}

// preparedTool is one composed operation as `codefly generate
// runnable-bindings` delivers it, round-tripped through its encoding; an empty
// tool name uses Core's default exposure.
func preparedTool(t *testing.T, endpoint, method, tool string, effect runnablev0.ToolExposure_Effect, actions ...string) *runnablev0.PreparedBinding {
	t.Helper()
	p := &runnablev0.PreparedBinding{
		Operation: &runnablev0.PreparedOperation{Module: "proposals", Service: "api", Endpoint: endpoint, Spelling: method},
		Call:      &runnablev0.PreparedCall{Address: "https://proposals.invalid", Route: &runnablev0.PreparedCall_Connect{Connect: &runnablev0.ConnectProcedure{Procedure: method}}},
		Contract: &basev0.RunnableContract{Protocol: resources.RunnableServiceProtocolV1,
			Input:  &basev0.RunnableSchema{Fields: []*basev0.RunnableField{{Name: "id", Type: basev0.RunnableField_STRING}}},
			Output: &basev0.RunnableSchema{Fields: []*basev0.RunnableField{{Name: "title", Type: basev0.RunnableField_STRING}}}},
		Policy: &runnablev0.Operation{AttemptTimeout: durationpb.New(10 * time.Second), TotalTimeout: durationpb.New(30 * time.Second), MaxAttempts: 1,
			Backoff: durationpb.New(time.Second), Audience: "proposals", InvokeScopes: []*basev0.WorkScopeV1{offerScope("proposals.item", actions...)},
			Completion: basev0.RunnableExecution_COMPLETION_CALL},
	}
	if tool != "" {
		p.Policy.Tool = &runnablev0.ToolExposure{Name: tool, Description: "A proposals tool.", Effect: effect}
	}
	raw, err := runnable.EncodePrepared(p)
	require.NoError(t, err)
	out, err := runnable.DecodePrepared(raw)
	require.NoError(t, err)
	return out
}

// proposalsOffer is an offer as the core reads it: a solution with two roles,
// a binding to a module, and the solution's own two methods.
func proposalsOffer(approval domain.ConsentApproval) authority.ReviewedOffer {
	m := domain.Module{Name: "proposals", Principal: "proposals", Kind: domain.KindSolution, Generation: 3,
		Roles: map[string][]*basev0.WorkScopeV1{
			"viewer": {offerScope("proposals.item", "read")},
			"editor": {offerScope("proposals.item", "read", "write")},
		},
		Bindings: map[string]domain.Binding{"documents": {Audience: "documents", InvokeScopes: []*basev0.WorkScopeV1{offerScope("example-records", "read")}}},
		Methods: map[string]domain.MethodPolicy{
			offerRead:              {Requires: []*basev0.WorkScopeV1{offerScope("proposals.item", "read")}},
			offerWrite:             {Requires: []*basev0.WorkScopeV1{offerScope("proposals.item", "write")}},
			"/tools.Solution/Open": {AnyCheckedCaller: true},
		},
	}
	return authority.ReviewedOffer{Approval: approval, Module: m, Modules: []domain.Module{m}}
}

// The projection is the local authority's: each role's operations, and the
// tool-exposed Connect bindings its scopes cover, one tool per name.
func TestAReviewedOfferShowsEachRolesOperationsAndTools(t *testing.T) {
	lookupRestricted := preparedTool(t, "connect", offerRead, "private_lookup", runnablev0.ToolExposure_EFFECT_READ_ONLY, "read")
	lookupRestricted.Policy.LookupScopes = []*basev0.WorkScopeV1{offerScope("proposals.private-outcome", "read")}
	lookupRestricted.Policy.InvokeScopes = append(lookupRestricted.Policy.InvokeScopes, lookupRestricted.Policy.LookupScopes...)
	raw, err := runnable.EncodePrepared(lookupRestricted)
	require.NoError(t, err)
	lookupRestricted, err = runnable.DecodePrepared(raw)
	require.NoError(t, err)
	bindings := map[string]*runnablev0.PreparedBinding{
		"PROPOSALS__API_CONNECT_LOOKUP":     lookupRestricted,
		"PROPOSALS__API_CONNECT_READ":       preparedTool(t, "connect", offerRead, "read_item", runnablev0.ToolExposure_EFFECT_READ_ONLY, "read"),
		"PROPOSALS__API_CONNECT_READ_AGAIN": preparedTool(t, "connect", offerRead, "", runnablev0.ToolExposure_EFFECT_READ_ONLY, "read"),
		"PROPOSALS__API_ALT_GRPC_READ":      preparedTool(t, "grpc", offerRead, "read_item", runnablev0.ToolExposure_EFFECT_READ_ONLY, "read"),
		"PROPOSALS__API_CONNECT_WRITE":      preparedTool(t, "connect", offerWrite, "write_item", runnablev0.ToolExposure_EFFECT_MUTATION, "write"),
	}
	approval := domain.ConsentApproval{TargetID: "proposals", Generation: 3, Digest: "sha256:offer"}
	review, err := reviewedOffer(proposalsOffer(approval), bindings)
	require.NoError(t, err)
	require.True(t, review.GetToolsComposed())
	require.Equal(t, "sha256:offer", review.GetApproval().GetDigest())
	require.Nil(t, review.GetInstallation(), "an uninstalled offer carries no installation")

	type toolInfo struct {
		operation, binding string
		write              bool
	}
	tools := map[string]map[string]toolInfo{}
	for _, role := range review.GetRoles() {
		tools[role.GetName()] = map[string]toolInfo{}
		for _, c := range role.GetCapabilities() {
			for _, tool := range c.GetTools() {
				require.NotContains(t, tools[role.GetName()], tool.GetName(), "duplicate tool exposure")
				tools[role.GetName()][tool.GetName()] = toolInfo{c.GetOperation(), tool.GetBinding(), tool.GetWrite()}
			}
		}
	}
	defaultTool, err := runnable.ToolFromPrepared(bindings["PROPOSALS__API_CONNECT_READ_AGAIN"])
	require.NoError(t, err)
	require.Equal(t, map[string]toolInfo{
		"read_item":           {offerRead, "PROPOSALS__API_CONNECT_READ", false},
		defaultTool.GetName(): {offerRead, "PROPOSALS__API_CONNECT_READ_AGAIN", defaultTool.GetEffect() != runnablev0.ToolExposure_EFFECT_READ_ONLY},
	}, tools["viewer"], "the viewer sees explicit and default tools, never Write, private lookup, or an any-caller operation")
	require.Equal(t, toolInfo{offerWrite, "PROPOSALS__API_CONNECT_WRITE", true}, tools["editor"]["write_item"])
	require.NotContains(t, tools["editor"], "private_lookup")

	require.Len(t, review.GetCeiling(), 1)
	require.Equal(t, "documents", review.GetCeiling()[0].GetModule())
	require.Equal(t, []string{"read"}, review.GetCeiling()[0].GetScopes()[0].GetActions())
}

// A read the core answers without the consent tuple carries none, and a host
// with no composed bindings says so rather than listing no tool silently.
func TestAReviewWithoutTheTupleCarriesNone(t *testing.T) {
	review, err := reviewedOffer(proposalsOffer(domain.ConsentApproval{}), nil)
	require.NoError(t, err)
	require.Nil(t, review.GetApproval())
	require.False(t, review.GetToolsComposed())
	for _, role := range review.GetRoles() {
		require.NotEmpty(t, role.GetCapabilities(), "role %s still reaches its operations", role.GetName())
		for _, c := range role.GetCapabilities() {
			require.Empty(t, c.GetTools())
		}
	}
}

// A failed shared projection is an authority refusal, not an empty tool list.
func TestAReviewedOfferPreservesTheSharedProjectionRefusal(t *testing.T) {
	binding := preparedTool(t, "connect", offerRead, "read_item", runnablev0.ToolExposure_EFFECT_READ_ONLY, "read")
	binding.Contract = nil
	_, err := reviewedOffer(proposalsOffer(domain.ConsentApproval{}), map[string]*runnablev0.PreparedBinding{"BROKEN": binding})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "the composed binding BROKEN is not a valid tool")
}
