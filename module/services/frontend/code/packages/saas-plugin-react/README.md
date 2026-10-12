# @codefly-dev/saas-plugin-react

Product-neutral React contribution composition and runtime adapters for trusted
compile-time plugins in the Codefly SaaS frontend host.

The contract package owns JSON-safe metadata. This package binds lazy React
components to the manifest's stable route and widget IDs:

```tsx
import {
  definePlugin,
  FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly-dev/saas-plugin-contract";
import { defineReactPlugin } from "@codefly-dev/saas-plugin-react";
import { lazy } from "react";

const manifest = definePlugin({
  contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
  name: "example",
  routes: [{ id: "overview", path: "/admin/example" }],
});

export const examplePlugin = defineReactPlugin({
  manifest,
  routes: [{ id: "overview", component: lazy(() => import("./overview.js")) }],
});
```

Every metadata ID must have exactly one registration. Missing, duplicate, and
extra registrations fail composition.

Product controllers use the injected service transport rather than importing
host auth state or constructing a backend URL:

```tsx
import { usePluginService } from "@codefly-dev/saas-plugin-react/runtime";

export function useTrafficRepository() {
  const service = usePluginService("example", "api");
  return () => service.request("traffic", {
    headers: { accept: "application/json" },
    query: { window: "24h" },
  });
}
```

The host supplies the current bearer through a closure. Product code cannot
read the host token accessor, replace `Authorization`, send cookies or trusted
identity/tenant headers, choose a backend origin, or escape the fixed
`/api/plugins/{plugin}/{alias}/…` same-origin route.

A relative path is one or more safe segments, and the **last** one may carry a
Google custom verb — `records:recover`,
`records/record-1/interactions/interaction-1:respond` — whose colon reaches the
upstream literal, because the colon separates the verb from the resource rather
than naming one. A Connect alias is reached the same way, by passing one
procedure path:

```tsx
const records = usePluginService("example", "records");
await records.request("records:recover", { method: "POST", body });

const procedures = usePluginService("example", "procedures");
await procedures.request("example.records.v1.RecordService/RecoverRecord", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: "{}",
});
```

A colon before the last segment, two colons in one segment, a traversal, or an
encoded separator throws `PluginTransportConfigurationError` with code
`invalid_path` before any request leaves the browser. The rule is the host's,
read from `@codefly-dev/saas-plugin-contract`'s `isFrontendServicePath` rather
than restated here, so a route this client admits is one the host forwards. The
full policy is in the module's
[BFF contract](../../../../docs/frontend-plugin-bff-contract.md).

Failed service responses can be converted into the public, non-sensitive
availability model before they enter a route or widget boundary:

```tsx
import {
  pluginErrorFromResponse,
  usePluginService,
} from "@codefly-dev/saas-plugin-react/runtime";

const service = usePluginService("example", "api");
const response = await service.request("traffic");
if (!response.ok) throw await pluginErrorFromResponse(response);
```

`service.capabilities()` uses the host's reserved BFF probe, validates the
protobuf-defined response, and caches only successful handshakes. The SaaS host
invokes it automatically for every service declared by the owning plugin before
rendering a route or widget. A missing operation or contract/major mismatch
becomes `incompatible`; an unresolved endpoint remains `unavailable`.

The host contains each contribution with the styling-neutral public boundary:

```tsx
import { PluginErrorBoundary } from "@codefly-dev/saas-plugin-react/ui";

<PluginErrorBoundary fallback={({ failure, retry }) => (
  <HostOwnedFailureView failure={failure} onRetry={retry} />
)}>
  <PluginRoute />
</PluginErrorBoundary>
```

`loading` is represented by the host Suspense fallback, `ready` by normal
rendering, and typed failures resolve to `unavailable`, `incompatible`, or
`failed`. Unknown render exceptions become the stable `render_failed` code.
Backend messages, URLs, stacks, and arbitrary response fields are never exposed
through the public failure descriptor. An optional request ID is accepted only
from the bounded host `x-request-id` response header; a product/backend problem
body cannot supply or replace public correlation metadata.

The package root, `./runtime`, and `./ui` are public. The `./ui` entry point
contains only generic containment primitives; the host continues to own all
visual styling and copy.
