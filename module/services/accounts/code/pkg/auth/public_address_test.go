package auth_test

import (
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/auth/egresstest"
)

// The classifier itself holds the vector set every egress guard is held to.
func TestIsPublicDestinationDialAddressHoldsTheEgressVectors(t *testing.T) {
	for _, v := range egresstest.Refused {
		t.Run("refuses/"+v.Name, func(t *testing.T) {
			if auth.IsPublicDestinationDialAddress(v.Address) {
				t.Fatalf("admitted %q", v.Address)
			}
		})
	}
	for _, v := range egresstest.Admitted {
		t.Run("admits/"+v.Name, func(t *testing.T) {
			if !auth.IsPublicDestinationDialAddress(v.Address) {
				t.Fatalf("refused %q", v.Address)
			}
		})
	}
}
