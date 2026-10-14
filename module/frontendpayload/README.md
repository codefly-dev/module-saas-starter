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

The supported federation profile uses `publicPath: "auto"`, a named global
remote entry, no executable public-path override and no remote dependencies.
Preparation verifies all manifest-listed synchronous/asynchronous JavaScript and
CSS, the declared expose, and client surfaces under `/assets/<bundle-file>`.
Both wire and cumulative expanded bytes are limited to 64 MiB. This checks the
producer's declared inventory; it does not analyze arbitrary JavaScript imports.
The prepared identity includes the complete signed frontend declaration.

Native processes read the render-owned `artifact-cache/ROOT` workspace value
through `ConfigureNative`; no root is inferred or created by the host. This
adapter requires both local Codefly context and absence of Pod context. An absent
source preserves pending delivery and never falls back to a backend's mutable
asset directory. Registry source selection and custody remain the deployment
operator's contract, not an implicit default.
