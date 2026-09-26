import {
	act,
	render,
	renderHook,
	screen,
	waitFor,
} from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { SolutionRequestBinding } from "../index.js";
import {
	requestBinding,
	SolutionBindingError,
	SolutionRequestError,
	solutionFetch,
	solutionJson,
	useAccessToken,
	useSolutionJson,
	useViewerEpoch,
	viewerIdentity,
} from "../index.js";

function token(claims: Record<string, unknown>): string {
	const body = btoa(JSON.stringify(claims))
		.replace(/\+/g, "-")
		.replace(/\//g, "_")
		.replace(/=+$/, "");
	return `header.${body}.signature`;
}

const alice = token({
	iss: "host",
	sub: "alice",
	org: "o1",
	auth_time: 1,
	sid: "s1",
});
const aliceRefreshed = token({
	iss: "host",
	sub: "alice",
	org: "o1",
	auth_time: 1,
	sid: "s2",
});
const bob = token({
	iss: "host",
	sub: "bob",
	org: "o1",
	auth_time: 2,
	sid: "s3",
});

describe("viewerIdentity", () => {
	it("is stable across a refresh for the same viewer and changes with the viewer", () => {
		expect(viewerIdentity(alice)).toBe(viewerIdentity(aliceRefreshed));
		expect(viewerIdentity(alice)).not.toBe(viewerIdentity(bob));
		expect(viewerIdentity(null)).toBeNull();
	});

	it("treats an unreadable credential as its own key", () => {
		expect(viewerIdentity("opaque")).toBe("opaque");
	});
});

describe("solutionFetch", () => {
	it("goes through the host's authed fetch, same-origin, with the bearer", async () => {
		const authedFetch = vi.fn(async () => new Response("{}"));
		await solutionFetch(
			{
				apiBase: "/api/solutions/x/proxy",
				getAccessToken: () => alice,
				authedFetch,
			},
			"/things?a=1",
			{ method: "POST", body: "{}" },
		);
		const [url, init] = authedFetch.mock.calls[0] as unknown as [
			string,
			RequestInit,
		];
		const headers = new Headers(init.headers);
		expect(url).toBe("/api/solutions/x/proxy/things?a=1");
		expect(init.credentials).toBe("same-origin");
		expect(init.method).toBe("POST");
		expect(headers.get("authorization")).toBe(`Bearer ${alice}`);
		expect(headers.get("accept")).toBe("application/json");
		expect(headers.get("content-type")).toBe("application/json");
	});

	it("falls back to fetch when the host provides no authed fetch", async () => {
		const fallback = vi.fn(async () => new Response("{}"));
		// stubGlobal, not assignment: a host test setup may make fetch read-only.
		vi.stubGlobal("fetch", fallback);
		try {
			await solutionFetch(
				{ apiBase: "/base", getAccessToken: () => null },
				"/x",
			);
		} finally {
			vi.unstubAllGlobals();
		}
		expect(fallback).toHaveBeenCalledOnce();
	});
});

it("cannot express a binding with no credential at all", () => {
	// The root `tsc --noEmit` covers these test files, so this @ts-expect-error IS
	// the regression test: let SolutionCredential admit a binding with neither
	// half and the expected error disappears, failing the typecheck.
	//
	// Without it a binding carrying neither a token getter nor an authed fetch
	// sends the request anonymously, and the host's bare `HTTP 401` is
	// indistinguishable from an expired session — a misconfigured remote
	// disguised as an ordinary sign-in prompt.
	const anonymous = {
		apiBase: "/base",
		// @ts-expect-error a binding with no credential source is not constructible
	} satisfies SolutionRequestBinding;
	expect(anonymous.apiBase).toBe("/base");
});

it("accepts the host's authedFetch alone, with no token getter", async () => {
	// A remote written against the binding before getAccessToken was required
	// passes { apiBase, authedFetch }: the host's authedFetch stamps the bearer
	// itself, so that binding is complete and must stay constructible.
	const seen: string[] = [];
	const authedFetch = async (input: RequestInfo | URL) => {
		seen.push(String(input));
		return new Response(JSON.stringify({ ok: true }), { status: 200 });
	};
	const binding = {
		apiBase: "/base",
		authedFetch,
	} satisfies SolutionRequestBinding;
	await expect(solutionJson(binding, "/things")).resolves.toEqual({ ok: true });
	expect(seen).toEqual(["/base/things"]);
});

it("refuses at run time a binding with no credential the type could not see", async () => {
	const anonymous = { apiBase: "/base" } as unknown as SolutionRequestBinding;
	await expect(solutionJson(anonymous, "/things")).rejects.toBeInstanceOf(
		SolutionBindingError,
	);
	expect(() => requestBinding("/base", undefined, undefined)).toThrow(
		SolutionBindingError,
	);
});

describe("solutionJson", () => {
	it("carries the backend's own error message and the status", async () => {
		const authedFetch = async () =>
			new Response(JSON.stringify({ error: "no organization selected" }), {
				status: 409,
			});
		const failure = await solutionJson(
			{ apiBase: "", getAccessToken: () => null, authedFetch },
			"/x",
		).catch((e: unknown) => e);
		expect(failure).toBeInstanceOf(SolutionRequestError);
		expect((failure as SolutionRequestError).status).toBe(409);
		expect((failure as SolutionRequestError).message).toBe(
			"no organization selected",
		);
	});

	it("reports the status when the backend sent no message", async () => {
		const authedFetch = async () => new Response("not json", { status: 502 });
		const failure = await solutionJson(
			{ apiBase: "", getAccessToken: () => null, authedFetch },
			"/x",
		).catch((e: unknown) => e);
		expect((failure as SolutionRequestError).message).toBe("HTTP 502");
	});
});

function Resource({
	getAccessToken,
	authedFetch,
}: {
	getAccessToken: () => string | null;
	authedFetch: typeof fetch;
}) {
	const state = useSolutionJson<{ who: string }>(
		{ apiBase: "/base", getAccessToken, authedFetch },
		"/who",
	);
	return <p>{state.status === "ready" ? state.data.who : state.status}</p>;
}

describe("useSolutionJson", () => {
	it("loads, then shows the answer", async () => {
		const authedFetch = vi.fn(
			async () => new Response(JSON.stringify({ who: "alice" })),
		);
		render(
			<Resource
				getAccessToken={() => alice}
				authedFetch={authedFetch as unknown as typeof fetch}
			/>,
		);
		expect(screen.getByText("loading")).toBeTruthy();
		await waitFor(() => expect(screen.getByText("alice")).toBeTruthy());
	});

	it("drops an answer that arrives after the viewer changed", async () => {
		let current = alice;
		let release: (response: Response) => void = () => {};
		const authedFetch = vi.fn(
			() =>
				new Promise<Response>((resolve) => {
					release = resolve;
				}),
		);
		render(
			<Resource
				getAccessToken={() => current}
				authedFetch={authedFetch as unknown as typeof fetch}
			/>,
		);
		current = bob;
		await act(async () => {
			release(new Response(JSON.stringify({ who: "alice" })));
		});
		expect(screen.queryByText("alice")).toBeNull();
	});
});

function Epoch({ getAccessToken }: { getAccessToken: () => string | null }) {
	return <p>epoch {useViewerEpoch(getAccessToken)}</p>;
}

describe("useAccessToken", () => {
	it("is told by the host's subscription instead of polling for a rotation", async () => {
		// The timer is the fallback for a host that cannot notify, and it runs for
		// as long as the page is open — one per observer. A host that passes
		// `subscribeToken` must switch it off, not run alongside it, so this pins
		// that no interval is armed and that the subscription alone drives a
		// re-read.
		const intervals: number[] = [];
		const realSetInterval = window.setInterval;
		vi.stubGlobal("setInterval", ((
			handler: TimerHandler,
			ms?: number,
			...rest: unknown[]
		) => {
			intervals.push(ms ?? 0);
			return realSetInterval(handler, ms, ...rest);
		}) as typeof window.setInterval);
		try {
			let current = alice;
			let notify = () => {};
			const unsubscribed = vi.fn();
			const subscribeToken = (listener: () => void) => {
				notify = listener;
				return unsubscribed;
			};
			const { result, unmount } = renderHook(() =>
				useAccessToken(() => current, subscribeToken),
			);
			expect(result.current).toBe(alice);
			expect(intervals).toEqual([]);

			current = bob;
			await act(async () => {
				notify();
			});
			expect(result.current).toBe(bob);

			unmount();
			expect(unsubscribed).toHaveBeenCalledOnce();
		} finally {
			vi.unstubAllGlobals();
		}
	});

	it("falls back to a timer when the host cannot notify", async () => {
		// An older host injects no subscription, and then re-reading is the only
		// way to notice: a rotation replaces neither the getter nor the host's
		// render, so nothing else would ever tell the remote.
		const intervals: number[] = [];
		const realSetInterval = window.setInterval;
		vi.stubGlobal("setInterval", ((
			handler: TimerHandler,
			ms?: number,
			...rest: unknown[]
		) => {
			intervals.push(ms ?? 0);
			return realSetInterval(handler, ms, ...rest);
		}) as typeof window.setInterval);
		try {
			renderHook(() => useAccessToken(() => alice));
			expect(intervals.length).toBeGreaterThan(0);
		} finally {
			vi.unstubAllGlobals();
		}
	});
});

describe("useViewerEpoch", () => {
	it("advances when the viewer changes and not on a refresh", async () => {
		let current = alice;
		render(<Epoch getAccessToken={() => current} />);
		expect(screen.getByText("epoch 0")).toBeTruthy();
		current = aliceRefreshed;
		await act(async () => {
			window.dispatchEvent(new Event("codefly:auth-changed"));
		});
		expect(screen.getByText("epoch 0")).toBeTruthy();
		current = bob;
		await act(async () => {
			window.dispatchEvent(new Event("codefly:auth-changed"));
		});
		expect(screen.getByText("epoch 1")).toBeTruthy();
	});
});
