# Policy log

`policy-log` is the independent witness for authority changes. Its contract is
append → signed receipt → commit-with-receipt in the caller. It assigns the
global sequence, owns the signing key, and records the committed prefix in its
own object store. A warehouse insert is only a mirror of that evidence.

**Implementation status:** the witness, GCS adapter, fixed-path key loader,
warehouse writer, transport-independent service, protobuf projection, gRPC
server and runnable binary are implemented and tested. The service is declared
in the module inventory and has its own private endpoint, service account, and
deployment declaration.

What is still missing is **delivery**, not code: the `policy-log` configuration
group ships no local default, so no environment here can boot this service, and
the signing key's fixed path exists on no development machine. Both refusals are
deliberate — see [Transport](#transport) for what that means for `codefly run`.
No generated code was hand-written: the required no-change Codefly
proto-generation check ran and came back byte-identical on two sibling services,
and this service's own tree regenerates reproducibly. The host's client is
separate work; no host Go code is changed here. Nothing in this directory has
been deployed or booted, and the OpenAPI projection does not exist because the
endpoint is private gRPC and reaches no gateway.

## Trust domain

The object store, signing key, and warehouse writer belong to the warehouse's
trust domain, independent of the database whose authority they witness. A
database restore must not restore the witness. Accounts must have no object
write, signing-key read, warehouse writer, or witness administration capability.
The witness holds no accounts database credential or dependency.

Delivery must provision a dedicated bucket, workload identity, private signing
key, and warehouse ingestion credential. Bucket IAM must confine the workload
to that bucket and forbid deleting or replacing `entries/` objects; enforce the
immutable prefix with retention and IAM, in addition to application checks.
The head's write authority is scoped to `head.json`. Bucket lifecycle deletion,
rewriting an existing sequence, or restoring the head from a database backup
would defeat the trust boundary and are not recovery procedures. Provision
versioning and deletion protection outside the workload's administrative reach.

The runtime exposes no delete, reset, unconditional overwrite, alternate key
path, insecure warehouse transport, or emulator option. The GCS constructor
refuses a storage emulator setting. The warehouse uses its own mandatory bearer
credential over HTTPS and refuses redirects.

The listener is private and must stay private. Composition must admit only the
host's declared client through workload-authenticated mesh policy, resolve its
endpoint through Codefly, and deliver credentials exclusively to that client and
this service. A shared credential available to arbitrary composed workloads is
not sufficient authority to append. No caller supplies sequence numbers or
receipt timestamps. `TestTheWitnessListenerStaysPrivate` holds the manifest to
that: a second endpoint, a declared `visibility`, a Connect listener, a REST api
or any service dependency fails it.

[service.codefly.yaml](service.codefly.yaml) uses the same `go-grpc` agent and
Codefly-allocated endpoint convention as the other Go services. Its `policy-log`
configuration group is delivered by composition; no default bucket, warehouse
credential, endpoint, or development bypass ships here. The fixed signing key
is a file projection, never a group setting. The service has no accounts or
store dependency and is not a public or module-visible interface export.

## Contract

The authored Go contract is [code/witness/contract.go](code/witness/contract.go).
The transport adapter exposes these four unary operations through
[code/service/service.go](code/service/service.go):

| Operation | Request | Response |
| --- | --- | --- |
| `Append` | `entry` | `Receipt` |
| `Cursor` | empty | signed `{head_seq, chain_hash, issued_at, signature}` |
| `Read` | `from_seq`, `limit` | ordered entries with their original receipts |
| `Keys` | empty | `{algorithm, key_id, public_key}` |

An entry consists of `operation_id`, `kind`, `subject`, `tenant`,
`narrowing_hash` (exactly 32 SHA-256 bytes), and `requested_at`. Identifiers are
opaque UTF-8 strings, with no control characters: operation, subject, and tenant
are at most 1024 bytes, kind at most 128. Tenant may be empty for a global
operation; it grants no authority. The other identifiers and timestamp are
required. The witness stores the hash, not the narrowing payload. Reconciliation
can identify an operation and verify its bytes; the host must retain or recover
the canonical operation bytes to apply it. A hash cannot reconstruct them.

`operation_id` identifies one logical operation for the lifetime of the log.
All entry fields, including `requested_at`, must stay identical on retry. The
same entry hash returns the exact original receipt, even after a restart or
later appends. Reusing the ID with a different hash is refused. `requested_at`
is the caller's logical timestamp, not a timeout or ordering authority; old
operations can be retried without changing their identity.

A receipt contains `seq` (nonzero uint64), `entry_hash`, `prev_hash`,
`chain_hash`, `issued_at`, and a 64-byte Ed25519 `signature`. All hashes are
32-byte values. The first sequence is 1; its previous hash is 32 zero bytes.
`issued_at` comes from the witness. Sequence, rather than clock time, orders
operations. `Read` starts at an **inclusive** sequence, at least 1, with a limit
from 1 through 500. Reading at `head_seq + 1` returns an empty page; asking
beyond it refuses, so an ahead-of-log reconciler cannot mistake rollback for
inactivity. A host API using an exclusive cursor maps it to `cursor + 1`.

### Canonical bytes

Signatures never depend on JSON serialization or protobuf field ordering.
`U64` and `U32` are unsigned big-endian integers. `S(text)` is its UTF-8 byte
length as U32 followed by those bytes. `T(time)` is Unix seconds as the
two's-complement bits of an int64, followed by nanoseconds as U32. Hashes are
raw bytes. Each domain below ends in one NUL byte, not the two characters
backslash and zero.

```text
entry_hash = SHA256(
  "policy-log.entry.v1\0" ||
  S(operation_id) || S(kind) || S(subject) || S(tenant) ||
  narrowing_hash || T(requested_at))

chain_hash = SHA256(
  "policy-log.chain.v1\0" || U64(seq) || prev_hash || entry_hash)

receipt_signature = Ed25519.Sign(private_key,
  "policy-log.receipt.v1\0" || U64(seq) ||
  entry_hash || prev_hash || chain_hash || T(issued_at))

cursor_signature = Ed25519.Sign(private_key,
  "policy-log.cursor.v1\0" || U64(head_seq) || chain_hash || T(issued_at))
```

Clients verify signatures under their independently pinned key, compare the
receipt's entry hash to their requested bytes, verify every chain link, and
persist the highest verified sequence and chain hash. A cursor is a signed
observation, not a receipt or a reset instruction. Never lower a persisted
cursor, accept a different hash at the same sequence, or substitute a fresh
network key for a pinned one. Signature verification alone cannot distinguish
a historical signed cursor from a current one; the host must also enforce its
freshness and reachability policy.

### Public key delivery

[identity.SigningKeyPath](code/identity/signing_key.go) is the single literal
fixed path. The loader takes no path argument, consults no environment override,
and never generates a key. Delivery provides exactly one PEM-encoded PKCS#8
Ed25519 private key, a regular file of at most 4096 bytes with no group or world
access, readable by the service UID. A missing, malformed, exposed, or wrong
algorithm key refuses startup. Projected-file symlinks are acceptable when the
opened target satisfies those checks.

The host receives `policy_log_public_key.json` under its existing trust-anchor
directory. Composition delivers that public document; this service must never
write the host's trust anchor. The schema matches `Keys`:

```json
{
  "algorithm": "Ed25519",
  "key_id": "lowercase SHA-256 hex of the raw public key",
  "public_key": "standard base64 of the 32-byte public key"
}
```

`Keys` publishes the public half, never private material. It does not authorize
trust-anchor replacement. Key rotation is deliberately not implemented: boot
with a different key refuses when the existing history fails verification.

## Transport

[proto/saas/policylog/v1/witness.proto](proto/saas/policylog/v1/witness.proto)
is the only wire projection of the Go contract, and
[code/server/server.go](code/server/server.go) is the only adapter. The server
owns the wire shape and the error translation and no policy of its own:
validation, sequencing, signing and the five-second budget stay behind the
service boundary. Regenerate with the command in
[proto/README.md](proto/README.md); never by hand and never with `buf generate`.

The proto carries every contract field losslessly, which is a requirement rather
than a convenience: the digests travel as raw bytes and the timestamps as
seconds and nanoseconds, so a reader can recompute `entry_hash` from the entry
it received, and a retry of one operation reproduces the same hash. The one
thing the projection has to add is a **refusal of an absent `requested_at`**:
`AsTime` of a missing Timestamp is 1970-01-01, which `Entry.Validate` accepts,
so without that check the witness would sign a hash over a timestamp the caller
never sent and no retry could reproduce it.

The five taxonomy errors are not interchangeable and each gets its own code. A
caller told the wrong one either gives up on a state that was about to clear or
retries one that never will:

| Contract error | Code | What the caller must do |
| --- | --- | --- |
| `ErrInvalid` | `InvalidArgument` | Never retry these bytes; they cannot be made to succeed |
| `ErrConflict` | `AlreadyExists` | Permanent: the operation id names different bytes. Do not retry, and do not rename the operation to get past it |
| `ErrPending` | `FailedPrecondition` | Do not retry this operation. Only an explicit retry of the operation holding the next sequence can clear it — and it does clear, which is why this is not `AlreadyExists` |
| `ErrRollback` | `Aborted` | A sequencer check failed: the head regressed, changed a known hash, or the caller is ahead of the log. Stop serving the authority still held locally |
| `ErrIntegrity` | `DataLoss` | The log's own stored evidence does not verify. Unrecoverable, and distinct from a regression |
| cancellation / deadline | `Canceled` / `DeadlineExceeded` | Genuinely ambiguous: a storage request in flight can have committed. Retry the identical operation and let the head answer |
| anything else | `Unavailable` | No sequence was assigned and no receipt exists; retry the identical operation |

`Unavailable` deliberately carries a fixed message. A store error can name a
bucket and an endpoint, the caller's correct behaviour does not depend on it,
and the detail goes to this service's own failure reporter instead.

[code/main.go](code/main.go) is the binary. Every construction happens **before**
`net.Listen`, in this order, and any failure is fatal: resolve the whole
configuration group, read the Codefly-allocated port, load the fixed-path
signing key, open the bucket, build the warehouse writer, start the mirror
worker, then `witness.Open`, which verifies the entire committed chain under
that key. A witness that accepted an append it could not sign, store, or verify
the history of would be worse than one that is down, because the caller would
commit against it.

`POLICY_LOG_BUCKET`, `POLICY_LOG_WAREHOUSE_ENDPOINT` and
`POLICY_LOG_WAREHOUSE_TOKEN` come from the `policy-log` configuration group and
from nowhere else. There is no process-environment fallback, no default bucket
and no development bypass, so an ambient value cannot make this service witness
somewhere nobody provisioned — and the refusal names the missing keys, never the
delivered values, because one of the three is a credential. The consequence is
that **`codefly run` cannot start this service on a workstation**: the group
ships no local default and the fixed key path exists on no development machine.
That is the posture the trust domain requires, not an oversight; a local harness
for it would be a development bypass by another name.

The listener registers the four witness operations and nothing else — no
reflection service and no health service — because its surface is the whole of
what a composed workload could reach if mesh policy ever leaked.

## Storage and failures

[objectstore.Store](code/objectstore/store.go) has two operations: a strongly
consistent read returning bytes and generation together, and a conditional
write. The production [GCS adapter](code/objectstore/gcs/gcs.go) uses the
official client, JSON reads, CRC32C uploads, no resumable upload, and
`RetryNever`. Tests use the [in-memory fake](code/objectstore/memory/memory.go).

Infrastructure initializes `head.json` once with the following JSON. The runtime
refuses an absent head instead of treating a deletion as an empty log:

```json
{"seq":0,"chain_hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
```

For each append, the witness reads the head generation and verifies any new
committed prefix. It prepares a receipt candidate, creates
`entries/00000000000000000001.json` (and succeeding fixed-width sequence names)
with `ifGenerationMatch=0`, then advances `head.json` using
`ifGenerationMatch=<observed generation>`. Only a successful head write makes
the entry visible to `Read` and allows `Append` to return its receipt. A
candidate's signature in the private object store is not yet an issued receipt.

Every conditional write has one attempt. A lost precondition is a refusal with
no receipt, never an internal retry with a newly minted sequence. Two appenders
over a prepared identical entry may race the head; exactly one wins. Two
different entries racing a sequence cannot overwrite one another.

| Failure | Result |
| --- | --- |
| Immutable entry creation fails | No head advance and no receipt |
| Entry persists but head write fails | Entry is invisible; no receipt |
| Head precondition is lost | Refusal, no retry, no receipt |
| Head commits but response is lost | Exact retry recovers the persisted receipt |
| Warehouse mirror fails or queue fills | Committed receipt remains valid; repair mirror from `Read` |
| Head is unavailable, regresses, or changes a known hash | Append, cursor, and reads refuse |

An uncommitted entry reserves its immutable sequence. Only an explicit retry of
that same operation and hash can finish it, using the original candidate. A
different operation refuses rather than deleting, overwriting, skipping, or
automatically committing the orphan. This sacrifices availability after an
interrupted append until its original operation is retried. It prevents a
recovery worker from performing a narrowing after its request has expired.

The in-memory index is rebuilt from the entire committed chain on boot. It is
not a durable authority. Every operation re-reads the head, including idempotent
appends and empty reads; cached evidence never hides an unreachable store.
Within a process the cursor only advances. Across restarts, deletion protection
and the host's persisted verified cursor remain necessary: a signing key alone
cannot detect a privileged operator restoring an older valid head and prefix.

### Five-second budget

`Append` bounds the caller's context to five seconds, including queueing and
catching up with another appender. Earlier deadlines win. Storage receives the
same cancellation context, and cancellation is checked before each mutation
and before returning any receipt. There is no detached append goroutine, retry,
or warehouse wait on the request path.

Cancellation is not a distributed abort. A GCS request already in flight can
have committed even when the response times out. The adapter cannot promise
that GCS itself never completes such a request after the local deadline; GCS
generation preconditions do not include a server-enforced expiration time. The
service withholds the receipt on timeout and reconciles the ambiguity through
the head on a later explicit retry/read. The fake proves that no expired write
is initiated or committed by a context-honoring store; it does not prove a
stronger remote cancellation guarantee. That distinction needs real-GCS
qualification before the transport can claim the full deadline requirement.

## Warehouse mirror

The [warehouse writer](code/mirror/warehouse.go) follows the repository's JSON
POST ingestion convention with a dedicated credential. The service offers an
entry only after the head commits. One worker consumes a bounded 64-entry
queue; failed and dropped mirrors are observable and recoverable from `Read`.
The mirror may contain duplicates after explicit retries and must deduplicate
by operation ID. It is best effort, is not backed by the accounts database,
and is never consulted to assign a sequence, sign a receipt, or reconstruct the
authoritative cursor.

## Verification

From `code/`:

```sh
go build ./...
go vet ./...
gofmt -l .
go test -race -v ./...
```

From `module/services/policy-log`, after any proto change and before it:

```sh
codefly generate proto --proto ./proto --output .. --template policy-log/proto/buf.gen.yaml
```

The witness tests independently encode the hashes and signatures and exercise
two appender instances over one generation-aware fake. The GCS adapter tests
intercept the official client's HTTP requests and verify its preconditions,
single-attempt failures, immutable entry names, bounded reads, and cancellation.
They do not contact GCS. Key tests refuse absent and invalid fixed-path keys;
service tests prove that a failed head write never offers an entry to the mirror
and a failed mirror cannot decide whether a receipt exists.

The transport is tested twice over. `code/server/server_test.go` drives each arm
of the error taxonomy directly, which is the only way to reach all of them, and
asserts that two arms never share a code and that no sentinel is left unmapped.
`code/server/transport_test.go` then runs the whole shipped stack — witness,
service, projection, generated client — over a real loopback gRPC connection, so
the arms are the ones the shipped code actually raises: a receipt that verifies
under the key `Keys` publishes, a retry returning the original receipt, a reused
operation id with different bytes as `AlreadyExists`, an interrupted append as
`FailedPrecondition` until its own retry finishes it, and a read at `head + 1`
empty while `head + 2` refuses.

Not exercised, by name: real GCS, deployed IAM and retention policy, the
container build, `codefly ci run`, `codefly run` and the Codefly service graph,
a deployed mesh policy, and a running host client. **Nothing here has been
deployed or booted** — the binary's own startup path (`codefly.Init`, the
injected port, the fixed-path key, the GCS client, `witness.Open` against a real
bucket) is covered by no test and has never executed. The signature of
`resolveSettings` is tested; its one caller's use of `WorkspaceValue` is not.

The fixed key-path literal still requires an explicit repository naming-policy
decision: the existing exemption for the host trust anchor does not cover this
new file. No gate or allowlist was weakened here.
