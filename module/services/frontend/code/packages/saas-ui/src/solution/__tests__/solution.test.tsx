import { act, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import {
	SolutionRequestError,
	solutionFetch,
	solutionJson,
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
		const original = globalThis.fetch;
		const fallback = vi.fn(async () => new Response("{}"));
		globalThis.fetch = fallback as unknown as typeof fetch;
		try {
			await solutionFetch({ apiBase: "/base" }, "/x");
		} finally {
			globalThis.fetch = original;
		}
		expect(fallback).toHaveBeenCalledOnce();
	});
});

describe("solutionJson", () => {
	it("carries the backend's own error message and the status", async () => {
		const authedFetch = async () =>
			new Response(JSON.stringify({ error: "no organization selected" }), {
				status: 409,
			});
		const failure = await solutionJson(
			{ apiBase: "", authedFetch },
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
			{ apiBase: "", authedFetch },
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
