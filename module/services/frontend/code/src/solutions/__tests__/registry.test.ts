import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// registry.ts is server-only; the marker package throws when imported outside a
// server component, so neutralize it for the unit test.
vi.mock("server-only", () => ({}));

// The registry resolves the gateway endpoint and the cluster-internal secret
// through the Codefly SDK; vi.mock is hoisted above module init.
const { getEndpoints, getWorkspaceSecret } = vi.hoisted(() => ({
  getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(() => []),
  getWorkspaceSecret:
    vi.fn<(name: string, key: string) => string | undefined>(),
}));
vi.mock("codefly", () => ({ getEndpoints, getWorkspaceSecret }));

import {
  findSolution,
  loadSolutions,
  parseManifest,
  registerSolution,
  unregisterSolution,
} from "@/solutions/registry";

function baseManifest(overrides: Record<string, unknown> = {}) {
  return {
    id: "audit",
    nav: { title: "Audit", path: "/s/audit" },
    frontend: {
      type: "module-federation",
      manifestUrl: "https://audit.internal/mf-manifest.json",
      exposedModule: "./Page",
    },
    ...overrides,
  };
}

describe("parseManifest", () => {
  it("accepts a well-formed manifest and defaults serviceAlias to id", () => {
    const parsed = parseManifest(baseManifest());
    expect(parsed).not.toBeNull();
    expect(parsed?.backend.serviceAlias).toBe("audit");
    expect(parsed?.frontend.manifestUrl).toBe(
      "https://audit.internal/mf-manifest.json",
    );
  });

  it("preserves an explicit serviceAlias", () => {
    const parsed = parseManifest(
      baseManifest({ backend: { serviceAlias: "audit-svc" } }),
    );
    expect(parsed?.backend.serviceAlias).toBe("audit-svc");
  });

  it("rejects a nav path that is not a safe in-app path", () => {
    for (const path of [
      "https://evil.example/x", // absolute off-site
      "//evil.example", // protocol-relative
      "javascript:alert(1)", // scheme, no leading slash
      "relative/path", // not absolute
      "/x y", // whitespace
      "/x\\y", // backslash
    ]) {
      expect(
        parseManifest(baseManifest({ nav: { title: "X", path } })),
        `path ${JSON.stringify(path)} must be rejected`,
      ).toBeNull();
    }
  });

  it("rejects a manifest URL that is not an absolute http(s) URL", () => {
    for (const manifestUrl of [
      "/relative/mf-manifest.json",
      "javascript:alert(1)",
      "data:text/javascript,alert(1)",
      "file:///etc/passwd",
      "https://user:pass@audit.internal/mf.json", // embedded credentials
    ]) {
      const manifest = baseManifest();
      (manifest.frontend as Record<string, unknown>).manifestUrl = manifestUrl;
      expect(
        parseManifest(manifest),
        `manifestUrl ${JSON.stringify(manifestUrl)} must be rejected`,
      ).toBeNull();
    }
  });

  it("rejects structurally invalid payloads", () => {
    expect(parseManifest(null)).toBeNull();
    expect(parseManifest({})).toBeNull();
    expect(parseManifest(baseManifest({ id: "" }))).toBeNull();
    const noExposed = baseManifest();
    (noExposed.frontend as Record<string, unknown>).exposedModule = "";
    expect(parseManifest(noExposed)).toBeNull();
  });
});

describe("parseManifest dashboard slot", () => {
  const validGraph = {
    events: [{ name: "login", type: "auth.login.v1" }],
    metrics: [
      {
        id: "logins",
        kind: "source",
        filter: { event: "login" },
        groupBy: "time",
        bucket: "day",
        aggregation: "count",
      },
    ],
    dashboards: [
      {
        id: "activity",
        layout: "grid",
        widgets: [{ id: "logins", metric: "logins", visualization: "line" }],
      },
    ],
  };

  it("omits the dashboard when the manifest declares none", () => {
    expect(parseManifest(baseManifest())?.dashboard).toBeUndefined();
  });

  it("carries a well-formed dashboard data graph through", () => {
    const parsed = parseManifest(baseManifest({ dashboard: validGraph }));
    expect(parsed?.dashboard?.dashboards[0]?.id).toBe("activity");
  });

  it("rejects the whole registration when the dashboard graph is malformed", () => {
    // A widget bound to a metric the graph never declares fails referential
    // integrity; the registration is refused rather than stored without it.
    const brokenGraph = {
      events: [],
      metrics: [],
      dashboards: [
        {
          id: "activity",
          layout: "grid",
          widgets: [{ id: "w", metric: "missing", visualization: "line" }],
        },
      ],
    };
    expect(parseManifest(baseManifest({ dashboard: brokenGraph }))).toBeNull();
  });
});

// The registry is no longer a map in this process: it is a cached projection of
// the durable record, read through the gateway. These cover what that shift put
// at risk — an outage must degrade rather than empty the navigation, and a
// half-registered solution must never reach the nav.
describe("registry snapshot", () => {
  const GATEWAY = "http://gateway.internal:8080";

  function manifestFor(id: string, order: number) {
    return JSON.stringify({
      id,
      nav: { title: id.toUpperCase(), path: `/s/${id}`, order },
      frontend: {
        type: "module-federation",
        manifestUrl: `https://${id}.internal/mf-manifest.json`,
        exposedModule: "./Page",
      },
      backend: { serviceAlias: id },
    });
  }

  function snapshotResponse(
    solutions: Array<{ id: string; status: string; manifest?: string }>,
  ) {
    return new Response(JSON.stringify({ revision: 7, solutions }), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }

  beforeEach(() => {
    const g = globalThis as Record<string, unknown>;
    g.__solutionSnapshot = null;
    g.__solutionSnapshotInFlight = null;
    getEndpoints.mockReturnValue([
      { service: "auth-gateway", name: "rest", address: `${GATEWAY}/rest` },
    ]);
    getWorkspaceSecret.mockReturnValue("internal-test-token");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    getEndpoints.mockReset();
    getWorkspaceSecret.mockReset();
  });

  it("serves active registrations ordered for the navigation", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        snapshotResponse([
          { id: "a", status: "active", manifest: manifestFor("a", 2) },
          { id: "b", status: "active", manifest: manifestFor("b", 1) },
        ]),
      ),
    );
    const solutions = await loadSolutions();
    expect(solutions).not.toBe("unavailable");
    expect((solutions as Array<{ id: string }>).map((s) => s.id)).toEqual([
      "b",
      "a",
    ]);
    expect(await findSolution("a")).toMatchObject({ nav: { title: "A" } });
  });

  // A solution that registered its page but not its backend is durable and
  // visible to an operator, and deliberately absent from the navigation.
  it("hides a registration that is not active", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        snapshotResponse([
          {
            id: "pending",
            status: "pending",
            manifest: manifestFor("pending", 1),
          },
          { id: "gone", status: "tombstoned" },
        ]),
      ),
    );
    expect(await loadSolutions()).toEqual([]);
    expect(await findSolution("pending")).toBeNull();
  });

  it("reports unavailable rather than empty when the gateway cannot be reached", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("unreachable");
      }),
    );
    expect(await loadSolutions()).toBe("unavailable");
    expect(await findSolution("a")).toBe("unavailable");
  });

  it("fails closed when no internal credential is configured", async () => {
    getWorkspaceSecret.mockReturnValue(undefined);
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    expect(await loadSolutions()).toBe("unavailable");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  // A registry outage must degrade, not delete: a blip after a good read keeps
  // serving the last snapshot instead of emptying the navigation.
  it("keeps the last snapshot when a refetch fails", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        snapshotResponse([
          { id: "a", status: "active", manifest: manifestFor("a", 1) },
        ]),
      )
      .mockRejectedValue(new Error("unreachable"));
    vi.stubGlobal("fetch", fetchMock);

    expect(await loadSolutions()).toHaveLength(1);
    (globalThis as Record<string, unknown>).__solutionSnapshot = {
      ...((globalThis as Record<string, unknown>).__solutionSnapshot as object),
      expiresAt: 0,
    };
    expect(await loadSolutions()).toHaveLength(1);
  });

  // Degrading on a blip is right; degrading forever is not. Past the gateway's
  // lease nothing in the held snapshot is provably still registered, so
  // continuing to serve it renders pages the gateway has already stopped
  // routing.
  it("stops serving a stale snapshot once it outlives the gateway lease", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        snapshotResponse([
          { id: "a", status: "active", manifest: manifestFor("a", 1) },
        ]),
      )
      .mockRejectedValue(new Error("unreachable"));
    vi.stubGlobal("fetch", fetchMock);

    expect(await loadSolutions()).toHaveLength(1);

    const g = globalThis as Record<string, unknown>;
    g.__solutionSnapshot = {
      ...(g.__solutionSnapshot as object),
      expiresAt: 0,
      fetchedAt: Date.now() - 120_001,
    };

    expect(await loadSolutions()).toBe("unavailable");
    expect(await findSolution("a")).toBe("unavailable");
  });

  it("drops a stored manifest that no longer validates", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        snapshotResponse([
          {
            id: "bad",
            status: "active",
            manifest: JSON.stringify({ id: "bad" }),
          },
        ]),
      ),
    );
    expect(await loadSolutions()).toEqual([]);
  });

  it("maps a registry refusal onto a typed write result", async () => {
    const manifest = parseManifest(JSON.parse(manifestFor("a", 1)));
    if (!manifest) throw new Error("fixture failed to parse");
    for (const [status, reason] of [
      [409, "conflict"],
      [403, "forbidden"],
      [500, "unavailable"],
    ] as const) {
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => new Response("", { status })),
      );
      expect(await registerSolution(manifest)).toEqual({ ok: false, reason });
    }
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ revision: 4, status: "active" }), {
            status: 200,
          }),
      ),
    );
    expect(await unregisterSolution("a")).toMatchObject({
      ok: true,
      revision: 4,
    });
  });
});
