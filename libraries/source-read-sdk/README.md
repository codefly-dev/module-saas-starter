# Accounts module-tier client

Generated Go client for Accounts' module tier (`ModuleCapabilitiesService` on
the `authority` endpoint): the connected-source seams (ClaimJobs, HeartbeatJob,
AckJob, NackJob, FetchDatasourceBlob, FetchDatasourceFiles,
MintSourceOperationContext, ListReadableSourceCollections) and the rest of the
module capability surface. Import
`github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go` at the
reviewed commit.

The bindings and the facade are `codefly generate client` output and are never
edited by hand. The library carries every descriptor it needs and imports no
other SDK, so a binary that links it registers each `saas.*` proto once. Do
not link it beside another copy of the same descriptors (an older SDK that
vendors `saas/events/v1` too): pick one.

```go
client, err := accounts.NewInternal(authorityURL, connect.WithInterceptors(workContext))
// Handle err before using client.
jobs, err := client.ModuleCapabilities().ClaimJobs(ctx, &accountsv1.ModuleClaimJobsRequest{Queue: "datasource"})
```

`NewInternal` selects HTTP/2 gRPC for the Accounts `authority` endpoint (h2c for
`http://`, certificate-verified TLS for `https://`). The facade skips streaming
RPCs; for FetchDatasourceBlob and FetchDatasourceFiles build the generated
`accountsv1connect` client on `InternalGateway(authorityURL)` with
`connect.WithGRPC()`.

## Credentials

Every call proves the calling module itself; no shared perimeter credential is
sent or accepted on this tier.

- The module-capability calls carry the module's own Work Context in
  `x-codefly-work-context`: the one the authority issues the workload for
  audience `module-capabilities` (Issue, from the workload's credential).
- ListReadableSourceCollections carries the original signed viewer Work
  Context instead: audience the calling module's registration prefix and
  kind-wide `read` on a resource type that module declares.
- ExchangeDelegatedReadAudience and ExchangeDelegatedOperationAudience carry
  the module's own Work Context in the header and the viewer parent only in the
  request body. Never substitute one for the other.
- MintSourceOperationContext authenticates with the module pod's workload
  credential in the request (`service_account_token`), which Accounts reviews
  and binds to the running build.

Never put a Work Context or a workload credential in a browser, and never log
request or response bodies.

## Source collections

Drain `next_page_token` until empty. Each collection has `source_id`,
`boundary_id`, `origin`, `container`, `ref`, and literal `paths`. Empty means
no current readable sources; any error invalidates a partial enumeration.
Changed source/grant or authorization revisions invalidate existing cursors.
`container` is what the consuming module's ingest keys the source's entries
under: the repository for a GitHub source, the source id for every other
provider.

## Regenerate

From the repository root:

```sh
codefly generate client --from saas-starter/accounts/authority --name source-read-sdk \
  --language go --services ModuleCapabilitiesService \
  --go-module github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go \
  --output libraries/source-read-sdk --force
```

Generation from this service's proto tree needs a CLI that names sources by
their buf module (codefly-dev/cli `2c6225ad`); earlier CLIs compile the
excluded `codefly/` tree and the vendored `codefly/base` under the wrong names
and fail.
