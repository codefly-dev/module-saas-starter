package main

import (
	"fmt"
	"strings"
)

// applyGeneratedAuthorizationMetadata joins a route to the generated method
// policy projection. Descriptor routes fail closed on a missing join. Unknown
// non-protobuf REST extensions retain their explicit extension policy.
func applyGeneratedAuthorizationMetadata(entry *RouteEntry) error {
	if entry == nil {
		return fmt.Errorf("route entry is nil")
	}
	procedure := entry.Procedure
	descriptorRESTRoute := false
	if procedure == "" {
		var ok bool
		procedure, ok = generatedRESTProcedureByRoute[strings.ToUpper(entry.Method)+" "+entry.Path]
		if !ok {
			return nil
		}
		descriptorRESTRoute = true
	}
	metadata, ok := generatedAuthorizationByProcedure[procedure]
	if !ok {
		return fmt.Errorf("generated authorization metadata is missing for %q", procedure)
	}
	if metadata.exposure == edgeExposureInternal {
		return fmt.Errorf("internal procedure %q cannot be exposed at the public edge", procedure)
	}
	expectedProtected := metadata.exposure != edgeExposurePublic
	if descriptorRESTRoute && entry.Protected != expectedProtected {
		return fmt.Errorf("REST overlay protected=%t disagrees with descriptor exposure %s for %q", entry.Protected, metadata.exposure, procedure)
	}
	entry.Procedure = procedure
	entry.Protected = expectedProtected
	entry.RateLimitClass = metadata.rateLimitClass
	entry.RateLimitBackendFailClosed = metadata.rateLimitBackendFailClosed
	entry.AuthenticationFactorAttempt = metadata.authenticationFactorAttempt
	entry.PolicySHA256 = metadata.policySHA256
	return nil
}
