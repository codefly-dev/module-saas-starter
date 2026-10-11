package business

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrSPIFFEIDMalformed reports a SPIFFE ID that is not a Kubernetes workload
// identity this host can read: spiffe://<trust domain>/ns/<namespace>/sa/<service account>.
var ErrSPIFFEIDMalformed = errors.New("not a Kubernetes workload SPIFFE ID")

// spiffeTrustDomainPattern is the SPIFFE trust-domain character set: lowercase
// letters, digits, dots, dashes and underscores — no port, no userinfo, no
// uppercase that a comparison would then disagree with.
var spiffeTrustDomainPattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

// KubernetesSPIFFEID is a parsed Kubernetes workload identity.
type KubernetesSPIFFEID struct {
	TrustDomain    string
	Namespace      string
	ServiceAccount string
}

// ParseKubernetesSPIFFEID is the ONE reader of a workload SPIFFE ID on this
// host: the envelope validator, the registry reconciler and the delivery
// carrier check all read the namespace and service account through it.
//
// It parses the whole URI rather than scanning for an "ns" segment. A scan
// reads `spiffe://ns/ns/acme-prod/sa/reports` as namespace "ns" — the trust
// domain — and binds an approved string to the wrong namespace; a port, a
// userinfo, an empty trust domain or a reordered or repeated path is a string
// that names no workload, refused rather than read leniently. The path is
// exactly /ns/<namespace>/sa/<service account>, each a DNS-1123 label, because
// that is the only shape a Kubernetes workload identity takes.
func ParseKubernetesSPIFFEID(id string) (KubernetesSPIFFEID, error) {
	refuse := func(reason string) (KubernetesSPIFFEID, error) {
		return KubernetesSPIFFEID{}, fmt.Errorf("%w: %q %s", ErrSPIFFEIDMalformed, id, reason)
	}
	rest, found := strings.CutPrefix(id, "spiffe://")
	if !found {
		return refuse("does not use the spiffe scheme")
	}
	trustDomain, path, found := strings.Cut(rest, "/")
	if !found {
		return refuse("has no path")
	}
	if trustDomain == "" || len(trustDomain) > 255 || !spiffeTrustDomainPattern.MatchString(trustDomain) {
		return refuse("does not name a trust domain (a port, a userinfo or an empty one is not one)")
	}
	if strings.ContainsAny(path, "?#%") {
		return refuse("carries a query, a fragment or an escape")
	}
	segments := strings.Split(path, "/")
	if len(segments) != 4 || segments[0] != "ns" || segments[2] != "sa" {
		return refuse("is not /ns/<namespace>/sa/<service account>")
	}
	for _, label := range []string{segments[1], segments[3]} {
		if len(label) > dns1123LabelMaxLength || !dns1123LabelPattern.MatchString(label) {
			return refuse("names a namespace or service account that is not a DNS-1123 label")
		}
	}
	return KubernetesSPIFFEID{TrustDomain: trustDomain, Namespace: segments[1], ServiceAccount: segments[3]}, nil
}
