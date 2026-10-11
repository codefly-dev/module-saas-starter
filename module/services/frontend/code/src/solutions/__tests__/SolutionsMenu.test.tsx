import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The menu's store is module state, so each test loads a fresh copy of the module
// with its own token and fetch stubs.
const tokenState = vi.hoisted(() => ({
	token: null as string | null,
	fetch: vi.fn<(input: string) => Promise<Response>>(),
}));

vi.mock("@/lib/connect/token-store", () => ({
	getToken: () => tokenState.token,
	authedFetch: (input: string) => tokenState.fetch(input),
	subscribeToken: (listener: () => void) => {
		window.addEventListener("codefly:auth-changed", listener);
		return () => window.removeEventListener("codefly:auth-changed", listener);
	},
}));

function token(claims: Record<string, string>): string {
	const payload = Buffer.from(JSON.stringify(claims), "utf8").toString(
		"base64url",
	);
	return `header.${payload}.signature`;
}

function signIn(claims: Record<string, string>) {
	tokenState.token = token(claims);
	window.dispatchEvent(new Event("codefly:auth-changed"));
}

function listing(...ids: string[]): Response {
	return Response.json({
		solutions: ids.map((id) => ({
			id,
			nav: { title: id.toUpperCase(), path: `/s/${id}` },
			available: true,
		})),
	});
}

async function mountMenu() {
	const { SolutionsMenu } = await import("@/solutions/SolutionsMenu");
	return render(<SolutionsMenu />);
}

async function settle() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

const ALICE = { sub: "user-alice", org: "org-acme" };
const BOB = { sub: "user-bob", org: "org-acme" };

describe("SolutionsMenu ownership (#949)", () => {
	beforeEach(() => {
		vi.resetModules();
		tokenState.token = token(ALICE);
		tokenState.fetch.mockReset();
	});

	afterEach(() => {
		cleanup();
	});

	it("does not show the previous viewer's list to the next one", async () => {
		tokenState.fetch.mockResolvedValueOnce(listing("audit"));
		await mountMenu();
		await settle();
		expect(screen.queryByText("AUDIT")).not.toBeNull();

		// Bob signs in on the same tab and his first poll cannot be answered.
		// Keeping "the last known list" would keep ALICE's.
		tokenState.fetch.mockResolvedValue(
			Response.json({ error: "authority_unavailable" }, { status: 503 }),
		);
		await act(async () => signIn(BOB));
		await settle();
		expect(screen.queryByText("AUDIT")).toBeNull();
	});

	it("discards a response that was started for a viewer who has since left", async () => {
		let answerAlice: (response: Response) => void = () => {};
		tokenState.fetch.mockReturnValueOnce(
			new Promise<Response>((resolve) => {
				answerAlice = resolve;
			}),
		);
		await mountMenu();

		tokenState.fetch.mockResolvedValue(listing("ledger"));
		await act(async () => signIn(BOB));
		await settle();
		// Alice's slow answer lands after the switch; it must not paint over Bob's.
		await act(async () => answerAlice(listing("audit")));
		await settle();
		expect(screen.queryByText("AUDIT")).toBeNull();
		expect(screen.queryByText("LEDGER")).not.toBeNull();
	});

	it("clears the list on a 401 the transport could not recover", async () => {
		vi.useFakeTimers();
		try {
			tokenState.fetch.mockResolvedValueOnce(listing("audit"));
			await mountMenu();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(0);
			});
			expect(screen.queryByText("AUDIT")).not.toBeNull();

			// authedFetch already tried to exchange the session and could not, so
			// the viewer is signed out. Their list must not stay on screen.
			tokenState.fetch.mockResolvedValue(
				Response.json({ error: "unauthenticated" }, { status: 401 }),
			);
			await act(async () => {
				await vi.advanceTimersByTimeAsync(10_000);
			});
			expect(tokenState.fetch).toHaveBeenCalledTimes(2);
			expect(screen.queryByText("AUDIT")).toBeNull();
		} finally {
			vi.useRealTimers();
		}
	});

	it("keeps the list through a 503 for the same viewer", async () => {
		vi.useFakeTimers();
		try {
			tokenState.fetch.mockResolvedValueOnce(listing("audit"));
			await mountMenu();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(0);
			});
			tokenState.fetch.mockResolvedValue(
				Response.json({ error: "authority_unavailable" }, { status: 503 }),
			);
			await act(async () => {
				await vi.advanceTimersByTimeAsync(10_000);
			});
			// An outage is not a sign-out: this viewer keeps their own list.
			expect(screen.queryByText("AUDIT")).not.toBeNull();
		} finally {
			vi.useRealTimers();
		}
	});

	it("keeps the list across a routine token refresh for the same viewer", async () => {
		tokenState.fetch.mockResolvedValueOnce(listing("audit"));
		await mountMenu();
		await settle();

		await act(async () => signIn({ ...ALICE, jti: "rotated" }));
		await settle();
		expect(screen.queryByText("AUDIT")).not.toBeNull();
		expect(tokenState.fetch).toHaveBeenCalledTimes(1);
	});
});
