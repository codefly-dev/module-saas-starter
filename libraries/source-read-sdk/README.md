# Source collection projection SDK

Generated Go client for the Accounts module-facing capability service. Import
`github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go` at the
reviewed commit. The standalone Go module has no consumer-local replacement.

```go
client, err := accounts.NewInternal(internalURL, authenticatedOptions...)
// Handle err before using client.
result, err := client.ModuleCapabilities().
    ListReadableSourceCollections(ctx, &accountsv1.ListReadableSourceCollectionsRequest{
        PageSize: 1000, PageToken: cursor,
    })
```

`NewInternal` selects HTTP/2 gRPC for the Accounts internal endpoint (h2c for
`http://`, certificate-verified TLS for `https://`). Credentials are supplied
through Connect interceptors. `New(gateway, options...)` also enforces gRPC;
a custom gateway must provide an HTTP/2-capable client.
Every call must carry one original signed viewer Work Context in
`x-codefly-work-context`, audience `documents`, kind-wide `documents/read`,
plus the cluster-internal perimeter credential. Installation/module credentials
are not a viewer. Never put either credential or a minting secret in a browser.
The empty request contains no tenant, subject, scopes, or boundary authority.

Drain `next_page_token` until empty. Each collection has `source_id`,
`boundary_id`, `origin`, `container`, `ref`, and literal `paths`. Empty means
no current readable sources; any error invalidates a partial enumeration.
Changed source/grant or authorization revisions invalidate existing cursors;
standing-grant expiration also requires a restart. Source pages are joined and
limited in the database, under the same snapshot as authority verification. Current owner and
all delegated actor grants are intersected in the authenticated tenant.
Unsupported providers fail explicitly; the initial projection supports GitHub.

A server adapter deriving the active session organization from authenticated
identity and exchanging a bearer for a viewer Work Context remains a separate
runtime seam. Never choose the first membership or trust browser scopes.

Regenerate from the repository root after exporting contracts:

```sh
codefly generate contracts saas-starter
(cd libraries/source-read-sdk/go && go run ./cmd/generate --root ../../..)
```

Use one generated binding set per protobuf full name in a Go binary. Applications
already embedding Accounts generated bindings should regenerate their owned
contract from this version instead of importing a second duplicate descriptor set.

The generator trims the descriptor set to the selected service’s transitive
imports before invoking Codefly, then pins the facade to the internal gRPC
protocol. Unrelated Accounts services are not generated.
