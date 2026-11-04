package main

import (
	"context"
	"errors"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func principalBinding(t *testing.T, principal string, generation uint64) *accountsv1.SolutionHostBindingState {
	t.Helper()
	b := moduleManifestBinding(t, endpointOf("documents", "rest", "rest"))
	m, ok := appliedManifest(b)
	require.True(t, ok)
	m.Kind = solutionhost.KindSolution
	m.Generation = generation
	if principal != "" {
		m.Authority = &solutionhost.Authority{Principal: principal}
	}
	body, err := solutionhost.Marshal(m)
	require.NoError(t, err)
	b.Applied.Generation.Document = string(body)
	b.Applied.Generation.Generation = generation
	b.Registration.Declared.Generation = generation
	b.Registration.Declared.Kind = accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION
	b.Registration.Status = accountsv1.SolutionRegistrationStatus_SOLUTION_REGISTRATION_STATUS_ACTIVE
	return b
}

func principalSnapshot(b *accountsv1.SolutionHostBindingState, record *accountsv1.SolutionRegistration) *solutionRegistrySnapshot {
	return snapshotFromAppliedBindings(&accountsv1.ListSolutionRegistrationsResponse{Registrations: []*accountsv1.SolutionRegistration{record}}, &accountsv1.ListSolutionHostBindingsResponse{Bindings: []*accountsv1.SolutionHostBindingState{b}})
}

func TestAppliedPrincipalRequiresTheExactAppliedRegistration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*accountsv1.SolutionHostBindingState, *accountsv1.SolutionRegistration)
		want  string
	}{
		{"principal differs from alias", func(*accountsv1.SolutionHostBindingState, *accountsv1.SolutionRegistration) {}, "example-runtime"},
		{"mismatched registration", func(_ *accountsv1.SolutionHostBindingState, r *accountsv1.SolutionRegistration) { r.Revision++ }, ""},
		{"successor generation", func(b *accountsv1.SolutionHostBindingState, _ *accountsv1.SolutionRegistration) {
			b.Applied.Generation.Generation++
		}, ""},
		{"different binding", func(b *accountsv1.SolutionHostBindingState, _ *accountsv1.SolutionRegistration) {
			b.BindingId = "acme.test.other"
		}, ""},
		{"removed", func(b *accountsv1.SolutionHostBindingState, _ *accountsv1.SolutionRegistration) {
			b.Applied.Removed = true
		}, ""},
		{"unreadable document", func(b *accountsv1.SolutionHostBindingState, _ *accountsv1.SolutionRegistration) {
			b.Applied.Generation.Document = "{"
		}, ""},
		{"tombstone", func(b *accountsv1.SolutionHostBindingState, r *accountsv1.SolutionRegistration) {
			r.TombstonedAt = timestamppb.Now()
			b.Registration = r
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := principalBinding(t, "example-runtime", 1)
			r := proto.Clone(b.Registration).(*accountsv1.SolutionRegistration)
			tc.alter(b, r)
			s := principalSnapshot(b, r)
			require.Equal(t, tc.want, s.principals[r.SolutionId])
			if tc.want == "" {
				require.Empty(t, s.routeEndpoints)
			} else {
				require.NotEmpty(t, s.routeEndpoints)
			}
		})
	}
	b := principalBinding(t, "", 1)
	s := principalSnapshot(b, b.Registration)
	require.Empty(t, s.principals, "no alias fallback when authority is absent")
	require.NotEmpty(t, s.routeEndpoints, "public routes do not require invented authority")
}

type principalRegistry struct {
	snapshot *solutionRegistrySnapshot
	err      error
}

func (r *principalRegistry) List(context.Context, *accountsv1.ListSolutionRegistrationsRequest) (*solutionRegistrySnapshot, error) {
	return r.snapshot, r.err
}

func TestPrincipalTravelsWithOneLeasedRoutingSnapshot(t *testing.T) {
	b := principalBinding(t, "example-runtime", 1)
	r := &principalRegistry{snapshot: principalSnapshot(b, b.Registration)}
	c := newSolutionRegistryCache(r)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.resolveEndpoint = func(context.Context, moduleEndpoint) (string, error) { return "http://localhost:8080", nil }
	require.NoError(t, c.refresh(t.Context()))
	first, result := c.resolveRouting(t.Context(), "documents")
	require.Equal(t, solutionRoutable, result)
	require.Equal(t, "example-runtime", first.Principal)
	require.Equal(t, "documents", first.Alias)
	// Replacing an authoritative snapshot replaces principal and endpoint together;
	// the previously returned resolution must retain its own principal.
	b = principalBinding(t, "example-successor", 2)
	r.snapshot = principalSnapshot(b, b.Registration)
	c.resolveEndpoint = func(context.Context, moduleEndpoint) (string, error) { return "http://localhost:9090", nil }
	require.NoError(t, c.refresh(t.Context()))
	next, result := c.resolveRouting(t.Context(), "documents")
	require.Equal(t, solutionRoutable, result)
	require.Equal(t, "example-successor", next.Principal)
	require.Equal(t, "http://localhost:9090", next.Upstream.String())
	require.Equal(t, "example-runtime", first.Principal)
	require.Equal(t, "http://localhost:8080", first.Upstream.String())
	r.err = errors.New("registry unavailable")
	now = now.Add(solutionRegistryLease + time.Second)
	stale, result := c.resolveRouting(t.Context(), "documents")
	require.Equal(t, solutionRegistryStale, result)
	require.Nil(t, stale, "an expired snapshot cannot supply an admission principal")
}
