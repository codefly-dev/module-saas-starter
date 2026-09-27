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

import type {
  SolutionEntitlement,
  ViewerEntitlements,
} from "@/solutions/entitlements";
import {
  browserManifestUrl,
  cachedProjection,
  entitledSolutions,
  findSolution,
  invalidateProjections,
  loadSolutions,
  navProjection,
  parseManifest,
  registerSolution,
  surfacesProjection,
  unregisterSolution,
} from "@/solutions/registry";

// The entitlement every projection now requires. A healthy one is the ordinary
// case: the org installed the solution and the viewer's team was granted it.
const granted: SolutionEntitlement = {
  id: "audit",
  healthy: true,
  scopeNodeId: "11111111-1111-1111-1111-111111111111",
};
const grantedButUnhealthy: SolutionEntitlement = { ...granted, healthy: false };

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

  it("rejects a manifest URL that is neither absolute http(s) nor a backend path", () => {
    for (const manifestUrl of [
      "relative/mf-manifest.json", // neither absolute nor root-relative
      "//evil.example/mf-manifest.json", // protocol-relative: another origin
      "/assets/../api/internal/solutions", // climbs out of the solution
      "/./assets/mf-manifest.json",
      "/assets/mf manifest.json", // whitespace
      "/assets\\mf-manifest.json", // backslash
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

  // The id is a slug everywhere it is used — it names the proxy base in a URL
  // path and it is echoed into a server log line — but it was the one slug
  // nothing checked, so any non-empty string got through.
  it("holds the id to the same slug rule every surface id answers to", () => {
    for (const id of [
      'x\nsolution registration: "audit" registered (revision 999, active)',
      "../../etc",
      "Audit",
      "has space",
      "-leading",
      "trailing-",
    ]) {
      expect(parseManifest(baseManifest({ id }))).toBeNull();
    }
    for (const id of ["audit", "a", "a-b_c9"]) {
      expect(parseManifest(baseManifest({ id }))?.id).toBe(id);
    }
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

describe("parseManifest client surfaces", () => {
  function surface(overrides: Record<string, unknown> = {}) {
    return {
      id: "footnote",
      client: "word",
      title: "Footnote",
      module: "/surfaces/word/footnote.js",
      contract: 1,
      ...overrides,
    };
  }

  it("omits surfaces when the manifest declares none", () => {
    expect(parseManifest(baseManifest())?.surfaces).toBeUndefined();
  });

  it("adds no key to the stored bytes of a manifest that declares none", () => {
    // The registry recognises a re-registration as a lease renewal by byte
    // identity. A slot materialised as [] rather than left absent would move
    // every existing registrant's bytes, turning every heartbeat into a
    // content change and churning every replica's cache.
    const parsed = parseManifest(baseManifest());
    // Assert the parse SUCCEEDED first: JSON.stringify(null) is "null", which
    // contains no "surfaces" either, so a parser that rejected every manifest
    // would satisfy the absence check while proving nothing.
    expect(parsed).not.toBeNull();
    expect(JSON.stringify(parsed)).not.toContain("surfaces");
  });

  it("carries a well-formed declaration through whole", () => {
    const parsed = parseManifest(
      baseManifest({
        surfaces: [
          surface({
            description: "Cite a claim.",
            applies: { tagged: ["policy"] },
            events: ["documents.entry.*"],
          }),
        ],
      }),
    );
    expect(parsed?.surfaces).toEqual([
      {
        id: "footnote",
        client: "word",
        title: "Footnote",
        description: "Cite a claim.",
        module: "/surfaces/word/footnote.js",
        contract: 1,
        applies: { tagged: ["policy"] },
        events: ["documents.entry.*"],
      },
    ]);
  });

  it("copies the declared arrays out of the caller's payload", () => {
    // The parsed manifest is what lands in the process-wide snapshot. Holding
    // the caller's own arrays would let whoever still has the request body
    // change what every later reader sees.
    const tagged = ["policy"];
    const events = ["a.*"];
    const parsed = parseManifest(
      baseManifest({ surfaces: [surface({ applies: { tagged }, events })] }),
    );
    events.push("injected");
    tagged.push("injected");
    expect(parsed?.surfaces?.[0]?.events).toEqual(["a.*"]);
    expect(parsed?.surfaces?.[0]?.applies).toEqual({ tagged: ["policy"] });
  });

  it("does not constrain which client kinds exist", () => {
    // The set of kinds is deployment configuration. A host that enumerated them
    // would need a release before a new kind could be addressed at all.
    const parsed = parseManifest(
      baseManifest({
        surfaces: [surface({ client: "some-kind-the-host-never-heard-of" })],
      }),
    );
    expect(parsed?.surfaces?.[0]?.client).toBe(
      "some-kind-the-host-never-heard-of",
    );
  });

  it("rejects a module that is not a path on the solution's own origin", () => {
    for (const path of [
      "https://evil.example/surface.js",
      "//evil.example/surface.js",
      "javascript:alert(1)",
      "data:text/javascript,alert(1)",
      "surfaces/word/footnote.js",
      "/surfaces/word/foot note.js",
      "/surfaces\\word.js",
    ]) {
      expect(
        parseManifest(baseManifest({ surfaces: [surface({ module: path })] })),
        `module ${JSON.stringify(path)} must be rejected`,
      ).toBeNull();
    }
  });

  it("rejects a malformed surface rather than dropping it", () => {
    // Dropping one would leave the solution registered and looking like it
    // offers nothing, which is indistinguishable from offering nothing.
    for (const broken of [
      surface({ id: "" }),
      surface({ id: "Foot Note" }),
      surface({ client: "" }),
      surface({ client: "Word" }),
      surface({ title: "" }),
      surface({ contract: 0 }),
      surface({ contract: "1" }),
      surface({ description: 7 }),
      surface({ applies: "sometimes" }),
      surface({ applies: { tagged: ["ok", 3] } }),
      surface({ events: "documents.entry.*" }),
      surface({ events: [""] }),
    ]) {
      expect(
        parseManifest(baseManifest({ surfaces: [broken] })),
        `${JSON.stringify(broken)} must be rejected`,
      ).toBeNull();
    }
    expect(parseManifest(baseManifest({ surfaces: {} }))).toBeNull();
    expect(parseManifest(baseManifest({ surfaces: [null] }))).toBeNull();
  });

  it("rejects two surfaces of one kind sharing an id", () => {
    // A client sees one kind, so it keys on (solution id, surface id); the
    // stored record is deduplicated per (client, id) to match. A duplicate
    // makes that key ambiguous, and which one wins would be an accident of
    // ordering — while the same id under a different kind is a distinct
    // surface no single client ever sees twice.
    expect(
      parseManifest(baseManifest({ surfaces: [surface(), surface()] })),
    ).toBeNull();
    expect(
      parseManifest(
        baseManifest({
          surfaces: [surface(), surface({ client: "slack" })],
        }),
      ),
    ).not.toBeNull();
  });
});

describe("browserManifestUrl", () => {
  function withManifestUrl(manifestUrl: string) {
    const manifest = baseManifest();
    (manifest.frontend as Record<string, unknown>).manifestUrl = manifestUrl;
    const parsed = parseManifest(manifest);
    if (parsed === null) throw new Error(`${manifestUrl} must parse`);
    return parsed;
  }

  it("serves a backend-relative manifest through this host's own origin", () => {
    // A pod cannot know an address the browser reaches; its backend path it
    // does know, and the host serves that same-origin through the proxy.
    expect(browserManifestUrl(withManifestUrl("/assets/mf-manifest.json"))).toBe(
      "/api/solutions/audit/proxy/assets/mf-manifest.json",
    );
  });

  it("takes the host-served form as registered", () => {
    expect(
      browserManifestUrl(
        withManifestUrl("/api/solutions/audit/proxy/assets/mf-manifest.json"),
      ),
    ).toBe("/api/solutions/audit/proxy/assets/mf-manifest.json");
  });

  it("does not let one solution's path borrow another's proxy base", () => {
    expect(
      browserManifestUrl(
        withManifestUrl("/api/solutions/other/proxy/assets/mf-manifest.json"),
      ),
    ).toBe(
      "/api/solutions/audit/proxy/api/solutions/other/proxy/assets/mf-manifest.json",
    );
  });

  it("loads an absolute manifest from where it was registered", () => {
    expect(
      browserManifestUrl(withManifestUrl("https://cdn.example/mf-manifest.json")),
    ).toBe("https://cdn.example/mf-manifest.json");
  });
});

describe("surfacesProjection", () => {
  function withSurfaces(surfaces: unknown[]) {
    const parsed = parseManifest(baseManifest({ surfaces }));
    if (parsed === null) throw new Error("fixture manifest must parse");
    return parsed;
  }

  const manifest = withSurfaces([
    {
      id: "footnote",
      client: "word",
      title: "Footnote",
      module: "/surfaces/word/footnote.js",
      contract: 1,
    },
    {
      id: "ask",
      client: "slack",
      title: "Ask",
      module: "/surfaces/slack/ask.js",
      contract: 2,
    },
  ]);

  it("projects only the asked-for kind, named by the solution", () => {
    expect(surfacesProjection(manifest, "word", granted)).toEqual({
      id: "audit",
      title: "Audit",
      // Without this the declared module path resolves against nothing.
      origin: "https://audit.internal",
      available: true,
      surfaces: [
        {
          id: "footnote",
          client: "word",
          title: "Footnote",
          description: undefined,
          module: "/surfaces/word/footnote.js",
          contract: 1,
          applies: undefined,
          events: undefined,
        },
      ],
    });
  });

  it("carries the origin without the manifest path it came from", () => {
    const projected = surfacesProjection(manifest, "word", granted);
    expect(projected?.origin).toBe("https://audit.internal");
    expect(JSON.stringify(projected)).not.toContain("mf-manifest.json");
  });

  it("reports nothing for a kind the solution does not serve", () => {
    expect(surfacesProjection(manifest, "excel", granted)).toBeNull();
    expect(surfacesProjection(withSurfaces([]), "word", granted)).toBeNull();
  });

  it("does not hand out a reference into the cached snapshot", () => {
    // The snapshot outlives the response and is read by every later caller,
    // including other client kinds reading the same manifest objects. A
    // shallow copy protects the top-level strings and silently shares the two
    // fields that are not strings, so this mutates those.
    const cached = withSurfaces([
      {
        id: "footnote",
        client: "word",
        title: "Footnote",
        module: "/surfaces/word/footnote.js",
        contract: 1,
        applies: { tagged: ["policy"] },
        events: ["documents.entry.*"],
      },
    ]);
    const surface = surfacesProjection(cached, "word", granted)?.surfaces[0];
    if (surface === undefined) throw new Error("expected one surface");
    surface.title = "Rewritten";
    surface.events?.push("injected");
    if (typeof surface.applies === "object") {
      surface.applies.tagged.push("injected");
    }
    expect(cached.surfaces?.[0]?.title).toBe("Footnote");
    expect(cached.surfaces?.[0]?.events).toEqual(["documents.entry.*"]);
    expect(cached.surfaces?.[0]?.applies).toEqual({ tagged: ["policy"] });
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
    return new Response(
      JSON.stringify({ revision: 7, leaseSeconds: 120, solutions }),
      {
        status: 200,
        headers: { "content-type": "application/json" },
      },
    );
  }

  beforeEach(() => {
    const g = globalThis as Record<string, unknown>;
    g.__solutionSnapshot = null;
    g.__solutionSnapshotInFlight = null;
    g.__solutionSnapshotFailure = null;
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

  // Every browser polling the navigation drives this read, so a registry
  // outage printed a line per read for as long as it lasted — and the
  // registration endpoint's own per-request line is switched off for that path
  // in next.config.mjs, which leaves this the only thing saying the registry is
  // unreachable. It has to stay readable, and it has to say when it ends.
  it("reports an unreadable registry as one condition, and its recovery", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const info = vi.spyOn(console, "info").mockImplementation(() => {});
    const g = globalThis as Record<string, unknown>;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("down", { status: 503 })),
    );
    for (let read = 0; read < 9; read++) {
      g.__solutionSnapshot = null;
      g.__solutionSnapshotInFlight = null;
      expect(await loadSolutions()).toBe("unavailable");
    }
    expect(error).toHaveBeenCalledTimes(1);
    expect(String(error.mock.calls[0]?.[0])).toContain("gateway answered 503");

    // Still failing at a milestone says so again; silence while it is still
    // broken is the other way this goes wrong.
    g.__solutionSnapshot = null;
    g.__solutionSnapshotInFlight = null;
    expect(await loadSolutions()).toBe("unavailable");
    expect(error).toHaveBeenCalledTimes(2);
    expect(String(error.mock.calls[1]?.[0])).toContain("after 10 reads");

    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        snapshotResponse([
          { id: "a", status: "active", manifest: manifestFor("a", 1) },
        ]),
      ),
    );
    g.__solutionSnapshot = null;
    g.__solutionSnapshotInFlight = null;
    expect(await loadSolutions()).not.toBe("unavailable");
    expect(String(info.mock.calls[0]?.[0])).toContain(
      "snapshot readable again after 10 failed reads",
    );
    error.mockRestore();
    info.mockRestore();
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

  // The ceiling must come from the gateway's own lease, not a copy of it: a
  // mirrored literal keeps the old bound when the gateway's lease changes.
  it("bounds staleness by the lease the gateway reported, not a local copy", async () => {
    const shortLease = new Response(
      JSON.stringify({
        revision: 7,
        leaseSeconds: 10,
        solutions: [
          { id: "a", status: "active", manifest: manifestFor("a", 1) },
        ],
      }),
      { status: 200, headers: { "content-type": "application/json" } },
    );
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(shortLease)
        .mockRejectedValue(new Error("unreachable")),
    );

    expect(await loadSolutions()).toHaveLength(1);

    // Older than the gateway's 10s lease, far younger than the 120s fallback.
    const g = globalThis as Record<string, unknown>;
    g.__solutionSnapshot = {
      ...(g.__solutionSnapshot as object),
      expiresAt: 0,
      fetchedAt: Date.now() - 11_000,
    };

    expect(await loadSolutions()).toBe("unavailable");
  });

  // Unbounded, a wedged gateway never settles the fetch. Readers coalesce onto
  // that promise and `__solutionSnapshotInFlight` is only cleared in .finally,
  // so one half-open connection stalls every later read in the process.
  it("bounds every registry request so a wedged gateway cannot stall the process", async () => {
    const seen: Array<RequestInit | undefined> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, init?: RequestInit) => {
        seen.push(init);
        return snapshotResponse([
          { id: "a", status: "active", manifest: manifestFor("a", 1) },
        ]);
      }),
    );

    await loadSolutions();
    const manifest = parseManifest(JSON.parse(manifestFor("a", 1)));
    if (!manifest) throw new Error("fixture failed to parse");
    await registerSolution(manifest);

    expect(seen.length).toBeGreaterThanOrEqual(2);
    for (const init of seen) {
      expect(
        init?.signal,
        "every registry request must carry an abort signal",
      ).toBeInstanceOf(AbortSignal);
    }
  });

  // The TTL and the ceiling are independent windows. Serving on the TTL alone
  // is only safe while the ceiling is the larger of the two, which nothing
  // guarantees once the gateway reports the lease.
  it("honours a lease shorter than the snapshot TTL", async () => {
    const shortLease = new Response(
      JSON.stringify({
        revision: 7,
        leaseSeconds: 1,
        solutions: [
          { id: "a", status: "active", manifest: manifestFor("a", 1) },
        ],
      }),
      { status: 200, headers: { "content-type": "application/json" } },
    );
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(shortLease)
        .mockRejectedValue(new Error("unreachable")),
    );
    expect(await loadSolutions()).toHaveLength(1);

    // Past the 1s lease but still inside the 5s TTL: the fast path must not
    // serve it just because the TTL has not elapsed.
    const g = globalThis as Record<string, unknown>;
    g.__solutionSnapshot = {
      ...(g.__solutionSnapshot as object),
      fetchedAt: Date.now() - 2_000,
      expiresAt: Date.now() + 3_000,
    };
    expect(await loadSolutions()).toBe("unavailable");
  });

  // The ceiling is a safety bound, so the far side must not be able to set it
  // to "effectively never".
  it("clamps an out-of-range reported lease", async () => {
    // 1e999 parses to Infinity; an unchecked `> 0` accepts it and the ceiling
    // silently never trips again.
    const absurd = new Response(
      `{"revision":7,"leaseSeconds":1e999,"solutions":[{"id":"a","status":"active","manifest":${JSON.stringify(manifestFor("a", 1))}}]}`,
      { status: 200, headers: { "content-type": "application/json" } },
    );
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(absurd)
        .mockRejectedValue(new Error("unreachable")),
    );
    expect(await loadSolutions()).toHaveLength(1);

    const g = globalThis as Record<string, unknown>;
    g.__solutionSnapshot = {
      ...(g.__solutionSnapshot as object),
      expiresAt: 0,
      fetchedAt: Date.now() - 3_600_001,
    };
    expect(await loadSolutions()).toBe("unavailable");
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

describe("surfacesProjection for a solution served through the host", () => {
  const manifest = (() => {
    const candidate = baseManifest({
      surfaces: [
        {
          id: "footnote",
          client: "word",
          title: "Footnote",
          module: "/assets/surfaces/footnote.js",
          contract: 1,
        },
      ],
    });
    (candidate.frontend as Record<string, unknown>).manifestUrl =
      "/assets/mf-manifest.json";
    const parsed = parseManifest(candidate);
    if (parsed === null) throw new Error("fixture manifest must parse");
    return parsed;
  })();

  it("resolves modules against this host's origin, under the solution's proxy", () => {
    expect(surfacesProjection(manifest, "word", granted, "https://app.example")).toMatchObject({
      origin: "https://app.example",
      surfaces: [{ module: "/api/solutions/audit/proxy/assets/surfaces/footnote.js" }],
    });
  });

  it("leaves the solution out when there is no host origin to resolve against", () => {
    expect(surfacesProjection(manifest, "word", granted)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Per-organization, per-viewer narrowing (issue #949)
// ---------------------------------------------------------------------------

function entitlements(
  overrides: Partial<ViewerEntitlements> & {
    solutions?: SolutionEntitlement[];
  } = {},
): ViewerEntitlements {
  const { solutions = [granted], ...rest } = overrides;
  return {
    org: "org-acme",
    viewer: "viewer-1",
    byId: new Map(solutions.map((entitlement) => [entitlement.id, entitlement])),
    revision: solutions.map((s) => `${s.id}:${s.healthy}`).join("|"),
    ...rest,
  };
}

function manifestFor(id: string) {
  const parsed = parseManifest(
    baseManifest({ id, nav: { title: id, path: `/s/${id}` } }),
  );
  if (parsed === null) throw new Error("fixture manifest must parse");
  return parsed;
}

describe("entitledSolutions", () => {
  it("leaves out a deployed solution the organization has not installed", () => {
    // The whole point of the narrowing: registered is not the same as usable.
    const registered = [manifestFor("audit"), manifestFor("ledger")];
    const pairs = entitledSolutions(registered, entitlements());
    expect(pairs.map(({ manifest }) => manifest.id)).toEqual(["audit"]);
  });

  it("gives two teams in one organization genuinely different sets", () => {
    const registered = [
      manifestFor("audit"),
      manifestFor("ledger"),
      manifestFor("intake"),
    ];
    // Same org, same registered set, different grants — which is the acceptance
    // case: the answer must be a function of the viewer, not of the deployment.
    const reviewers = entitlements({
      viewer: "viewer-in-reviewers",
      solutions: [granted],
    });
    const clerks = entitlements({
      viewer: "viewer-in-clerks",
      solutions: [
        { id: "ledger", healthy: true, scopeNodeId: "node-ledger" },
        { id: "intake", healthy: true, scopeNodeId: "node-intake" },
      ],
    });
    expect(
      entitledSolutions(registered, reviewers).map(({ manifest }) => manifest.id),
    ).toEqual(["audit"]);
    expect(
      entitledSolutions(registered, clerks).map(({ manifest }) => manifest.id),
    ).toEqual(["ledger", "intake"]);
  });

  it("narrows after a revocation", () => {
    const registered = [manifestFor("audit"), manifestFor("ledger")];
    const before = entitlements({
      solutions: [
        granted,
        { id: "ledger", healthy: true, scopeNodeId: "node-ledger" },
      ],
    });
    expect(entitledSolutions(registered, before)).toHaveLength(2);
    // The grant on `ledger` is revoked: the authority stops returning it, so the
    // projection stops carrying it. Nothing about the registration changed.
    const after = entitlements({ solutions: [granted] });
    expect(
      entitledSolutions(registered, after).map(({ manifest }) => manifest.id),
    ).toEqual(["audit"]);
  });

  it("ignores an entitlement for a solution this deployment does not serve", () => {
    // An org can hold an installation for a solution that was deregistered or
    // has not registered yet. That is not an error to report into a menu.
    const pairs = entitledSolutions(
      [manifestFor("audit")],
      entitlements({
        solutions: [
          granted,
          { id: "retired", healthy: true, scopeNodeId: "node-retired" },
        ],
      }),
    );
    expect(pairs.map(({ manifest }) => manifest.id)).toEqual(["audit"]);
  });
});

describe("projection availability", () => {
  it("keeps an unhealthy installation visible and marks it unavailable", () => {
    // Visible, because the org installed it and the viewer was granted it —
    // hiding it sends someone looking for a grant that already exists. Not
    // available, because it must not be routed as though it were serving.
    expect(navProjection(manifestFor("audit"), grantedButUnhealthy)).toEqual({
      id: "audit",
      nav: { title: "audit", path: "/s/audit" },
      available: false,
    });
  });

  it("marks a healthy installation available", () => {
    expect(navProjection(manifestFor("audit"), granted).available).toBe(true);
  });
});

describe("cachedProjection", () => {
  beforeEach(() => {
    invalidateProjections();
  });

  it("recomputes when the authority revision moves", () => {
    const compute = vi.fn(() => ["audit"]);
    const before = entitlements();
    expect(cachedProjection(before, "word", 1, compute)).toEqual(["audit"]);
    // Same key: reused.
    cachedProjection(before, "word", 1, compute);
    expect(compute).toHaveBeenCalledTimes(1);
    // A revoke moves the revision, so the key moves and the menu is recomputed.
    // Without this a cached projection outlives the grant that justified it —
    // the one failure this cache key exists to prevent.
    cachedProjection(entitlements({ solutions: [] }), "word", 1, compute);
    expect(compute).toHaveBeenCalledTimes(2);
  });

  it("does not share a projection between organizations, viewers or kinds", () => {
    const compute = vi.fn(() => ["audit"]);
    const base = entitlements();
    cachedProjection(base, "word", 1, compute);
    cachedProjection({ ...base, org: "org-other" }, "word", 1, compute);
    cachedProjection({ ...base, viewer: "viewer-2" }, "word", 1, compute);
    cachedProjection(base, "excel", 1, compute);
    // Four distinct keys — one per dimension the projection depends on.
    expect(compute).toHaveBeenCalledTimes(4);
  });

  it("recomputes when the registered set moves under an unchanged grant", () => {
    const compute = vi.fn(() => ["audit"]);
    const base = entitlements();
    cachedProjection(base, "word", 1, compute);
    // A re-registration with a new nav title moves no grant, so the authority
    // revision is identical; the registry revision is what must catch it.
    cachedProjection(base, "word", 2, compute);
    expect(compute).toHaveBeenCalledTimes(2);
  });
});
