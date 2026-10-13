package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

const workloadPresenterHeader = "x-codefly-workload-presenter"

type workloadPresenterKey struct{}

// presenterFromXFCC accepts only the shape the protected waypoint synthesizes.
// The By comparison is useful only together with enforced network provenance:
// the destination must admit this listener from that waypoint alone, and every
// admitted path must run its HTTP overwrite filter. A caller header is no proof.
func presenterFromXFCC(values []string, waypoint string) (string, error) {
	if waypoint == "" || len(values) != 1 || len(values[0]) > 2048 {
		return "", errors.New("workload presenter is unavailable")
	}
	by, uri, ok := strings.Cut(values[0], ";URI=")
	if !ok || by != "By="+waypoint || strings.ContainsAny(uri, ";,\" \t\r\n") {
		return "", errors.New("workload presenter is malformed")
	}
	principal, err := url.Parse(uri)
	if err != nil || principal.Scheme != "spiffe" || principal.Host == "" || principal.User != nil || principal.Port() != "" ||
		principal.ForceQuery || principal.RawQuery != "" || principal.Fragment != "" || principal.RawPath != "" || principal.Opaque != "" || principal.Path == "" || principal.String() != uri {
		return "", errors.New("workload presenter is malformed")
	}
	// Accounts owns the Kubernetes principal grammar and compares the exact
	// authenticated value to both applied workload and TokenReview identity.
	return uri, nil
}

func workloadPresenter(ctx context.Context) string {
	principal, _ := ctx.Value(workloadPresenterKey{}).(string)
	return principal
}
