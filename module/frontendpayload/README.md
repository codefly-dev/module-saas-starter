# Host frontend bundles

Accounts and the gateway share this preparer. It reads the complete signed
`application/x-tar` payload, verifies its digest, uses Core's bounded archive
extractor and snapshots private bytes before exposing a file. An unavailable or
invalid source cannot replace an already prepared bundle.

`RegistrySource` reads blobs from one operator-selected OCI repository through
ORAS. Reader credentials are values. Redirects and foreign authentication realms
are refused. HTTPS is explicit; plain HTTP requires the mesh Service assertion
or a loopback development source. No registry is selected by default.

`DirectorySource` reads Codefly's existing native artifact cache, whose files are
named by the SHA-256 hex digest. It never writes a cache and is refused when the
host reports that it runs in a Pod. Neither adapter is an artifact store:
production retention and reader access remain the configured repository's duty.

Run `GOWORK=off go test -race ./...` here. These tests cover preparation and
transport; they do not qualify host activation, a browser rollout or deployment.
