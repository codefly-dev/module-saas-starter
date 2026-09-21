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
  surfacesProjection,
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
    expect(surfacesProjection(manifest, "word")).toEqual({
      id: "audit",
      title: "Audit",
      // Without this the declared module path resolves against nothing.
      origin: "https://audit.internal",
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
    const projected = surfacesProjection(manifest, "word");
    expect(projected?.origin).toBe("https://audit.internal");
    expect(JSON.stringify(projected)).not.toContain("mf-manifest.json");
  });

  it("reports nothing for a kind the solution does not serve", () => {
    expect(surfacesProjection(manifest, "excel")).toBeNull();
    expect(surfacesProjection(withSurfaces([]), "word")).toBeNull();
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
    const surface = surfacesProjection(cached, "word")?.surfaces[0];
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
