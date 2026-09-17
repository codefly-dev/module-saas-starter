package adapters

import "crypto/tls"

// hasCustodyIdentity reports whether tc can present a server leaf: a static one
// in Certificates, or a reloading one through GetCertificate. The owning host
// supplies the latter so a rotated workload leaf is served without a restart.
func hasCustodyIdentity(tc *tls.Config) bool {
	return tc != nil && (len(tc.Certificates) > 0 || tc.GetCertificate != nil)
}

// custodyListenerTLS derives the configuration every custody listener serves
// from the identity the owning host supplies. When that identity is a reloading
// GetCertificate, no static leaf may ride along in Certificates: Go consults
// GetCertificate only when Certificates is empty or the client sent an SNI
// name, so a peer dialing by IP (a kubelet probe, an in-cluster IP dial) would
// otherwise be served the boot-time leaf and the reload defeated for exactly
// those peers.
func custodyListenerTLS(tc *tls.Config) *tls.Config {
	config := tc.Clone()
	config.MinVersion = tls.VersionTLS13
	config.GetConfigForClient = nil
	if config.GetCertificate != nil {
		config.Certificates = nil
	}
	return config
}
