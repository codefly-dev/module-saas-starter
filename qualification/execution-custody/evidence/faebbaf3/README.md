# Private HTTPS signing JWKS qualification

Clean source `faebbaf320ff0be75b6cfaae0e15c815e66b9b41` based on merged
`2cd6caeb95c44670aa37d543e6c8ccff2e317762`.

`python3 qualification/execution-custody/run.py --tenant-mount` passed with
race detection, real local PostgreSQL (all 127 migrations), Vault and Redis.
The normal host publishes its current and prior signing public keys over verified
TLS 1.3. An actual tenant-issued Work Context verifies with the fetched set.
Wrong HTTP methods, wrong TLS trust/hostname/version, missing custody credentials,
revision/tenant exposure and revocation/lifecycle checks pass. See acceptance.log.
The runner records 8 source hashes, migration hashes and its exact test binary.

Additional focused checks passed: `go test -race ./pkg/adapters -run
'^TestJWKSHTTPHandler' -count=1` (document, failure and method handling),
`go vet .` and normal service `go build`. The naming gate and diff check pass.
This proof substitutes loopback network discovery and disposable DB/Vault/Redis
configuration; it is not a full Codefly bootstrap, managed transport proof,
new Linux release image or hosted acceptance. No paid or cloud operations ran.
