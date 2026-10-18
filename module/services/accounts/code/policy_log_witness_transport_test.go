package main

import (
	"errors"
	"strings"
	"testing"
)

type meshAssertion map[string]string

func (m meshAssertion) WorkspaceValue(group, key string) (string, error) {
	value, ok := m[group+"/"+key]
	if !ok {
		return "", errors.New("not configured")
	}
	return value, nil
}

// TestThePolicyLogWitnessIsDialledInPlaintextOnlyUnderTheMesh: handbook security
// posture rule 1. The dial carries no transport security of its own, so it is
// admitted only when the composition asserts the mesh AND the address is an
// in-cluster Service; anything else is refused by name, never dialled.
func TestThePolicyLogWitnessIsDialledInPlaintextOnlyUnderTheMesh(t *testing.T) {
	asserted := meshAssertion{"internal-transport/mesh-protected": "true"}
	if err := policyLogWitnessTransport(asserted, "policy-log.platform-saas.svc.cluster.local:9090"); err != nil {
		t.Fatalf("an in-cluster Service under the mesh assertion was refused: %v", err)
	}
	for name, tc := range map[string]struct {
		source meshAssertion
		host   string
	}{
		"no assertion":   {meshAssertion{}, "policy-log.platform-saas.svc.cluster.local:9090"},
		"asserted false": {meshAssertion{"internal-transport/mesh-protected": "false"}, "policy-log.platform-saas.svc:9090"},
		"loopback":       {asserted, "localhost:9090"},
		"public suffix":  {asserted, "policy-log.platform-saas.svc.example.com:9090"},
		"bare name":      {asserted, "policy-log:9090"},
	} {
		t.Run(name, func(t *testing.T) {
			err := policyLogWitnessTransport(tc.source, tc.host)
			if err == nil || !strings.Contains(err.Error(), "refusing cleartext gRPC to the policy-log witness") {
				t.Fatalf("dial to %q admitted or refused without naming why: %v", tc.host, err)
			}
		})
	}
}
