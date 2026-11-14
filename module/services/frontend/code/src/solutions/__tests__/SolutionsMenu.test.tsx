import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The menu's store is module state, so each test loads a fresh copy of the module
// with its own token and fetch stubs.
const tokenState = vi.hoisted(() => ({
	token: null as string | null,
	// Each narrowed bearer, by the session it belongs to.
	narrowed: new Map<string, string>(),
	fetch: vi.fn<(input: string) => Promise<Response>>(),
}));

vi.mock("@/lib/connect/token-store", () => ({
	getToken: () => tokenState.token,
	narrowedSession: (token: string | null) =>
		(token !== null && tokenState.narrowed.get(token)) || "",
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
		tokenState.narrowed.clear();
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

	// After a person switch the first read could meet a session the host had
	// not settled yet; the menu showed no page at all until the next 10 s poll.
	it("asks again within a second when a viewer's first read is not answered", async () => {
		vi.useFakeTimers();
		try {
			tokenState.fetch
				.mockResolvedValueOnce(
					Response.json({ error: "authority_unavailable" }, { status: 503 }),
				)
				.mockResolvedValue(listing("ledger"));
			await mountMenu();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(0);
			});
			expect(screen.queryByText("LEDGER")).toBeNull();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(1_000);
			});
			expect(screen.queryByText("LEDGER")).not.toBeNull();
			expect(tokenState.fetch).toHaveBeenCalledTimes(2);
			// Answered: no quick retries any more, only the poll.
			await act(async () => {
				await vi.advanceTimersByTimeAsync(5_000);
			});
			expect(tokenState.fetch).toHaveBeenCalledTimes(2);
		} finally {
			vi.useRealTimers();
		}
	});

	it("stops asking quickly after a few unanswered first reads", async () => {
		vi.useFakeTimers();
		try {
			tokenState.fetch.mockResolvedValue(
				Response.json({ error: "authority_unavailable" }, { status: 503 }),
			);
			await mountMenu();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(9_000);
			});
			// The first read and five quick retries; then only the 10 s poll.
			expect(tokenState.fetch).toHaveBeenCalledTimes(6);
		} finally {
			vi.useRealTimers();
		}
	});

	it("says the first read failed once its quick retries are spent, until a poll answers", async () => {
		vi.useFakeTimers();
		try {
			tokenState.fetch.mockResolvedValue(
				Response.json({ error: "authority_unavailable" }, { status: 503 }),
			);
			const { useRegisteredSolutionsRead } = await import(
				"@/solutions/SolutionsMenu"
			);
			function Read() {
				return <p>{useRegisteredSolutionsRead()}</p>;
			}
			render(<Read />);
			expect(screen.getByText("reading")).toBeTruthy();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(4_500);
			});
			expect(screen.getByText("reading")).toBeTruthy();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(1_000);
			});
			expect(screen.getByText("failed")).toBeTruthy();
			tokenState.fetch.mockResolvedValue(listing("audit"));
			await act(async () => {
				await vi.advanceTimersByTimeAsync(10_000);
			});
			expect(screen.getByText("read")).toBeTruthy();
		} finally {
			vi.useRealTimers();
		}
	});

	it("says the first read failed at once on a 429, which is not asked again quickly", async () => {
		vi.useFakeTimers();
		try {
			tokenState.fetch.mockResolvedValue(
				Response.json({ error: "rate_limited" }, { status: 429 }),
			);
			const { useRegisteredSolutionsRead } = await import(
				"@/solutions/SolutionsMenu"
			);
			function Read() {
				return <p>{useRegisteredSolutionsRead()}</p>;
			}
			render(<Read />);
			await act(async () => {
				await vi.advanceTimersByTimeAsync(0);
			});
			expect(screen.getByText("failed")).toBeTruthy();
		} finally {
			vi.useRealTimers();
		}
	});

	it("starts the next person's quick retries afresh, and drops the last person's", async () => {
		vi.useFakeTimers();
		try {
			tokenState.fetch.mockResolvedValue(
				Response.json({ error: "authority_unavailable" }, { status: 503 }),
			);
			await mountMenu();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(500);
			});
			// Alice's retry is waiting when Bob signs in.
			await act(async () => signIn(BOB));
			const afterSwitch = tokenState.fetch.mock.calls.length;
			await act(async () => {
				await vi.advanceTimersByTimeAsync(5_500);
			});
			// Bob's read on the switch plus his own five retries; Alice's timer is gone.
			expect(tokenState.fetch.mock.calls.length - afterSwitch).toBe(5);
		} finally {
			vi.useRealTimers();
		}
	});

	it("does not ask again quickly on a 429, the organization's read budget", async () => {
		vi.useFakeTimers();
		try {
			tokenState.fetch.mockResolvedValue(
				Response.json({ error: "rate_limited" }, { status: 429 }),
			);
			await mountMenu();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(5_000);
			});
			expect(tokenState.fetch).toHaveBeenCalledTimes(1);
		} finally {
			vi.useRealTimers();
		}
	});

	it("leaves no retry behind when the last menu unmounts", async () => {
		vi.useFakeTimers();
		try {
			tokenState.fetch.mockResolvedValue(
				Response.json({ error: "authority_unavailable" }, { status: 503 }),
			);
			const view = await mountMenu();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(0);
			});
			view.unmount();
			await act(async () => {
				await vi.advanceTimersByTimeAsync(5_000);
			});
			expect(tokenState.fetch).toHaveBeenCalledTimes(1);
		} finally {
			vi.useRealTimers();
		}
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

	// A cast is the team's pages and none of the person's own: the server
	// narrows on the session, so the menu is the cast's list, read at once,
	// never the person's list kept until the next poll. Red before: the cast
	// bearer carried the same subject and organization, so the person's list
	// stayed on screen for up to one poll after "Act as this team".
	it("re-reads the list at once for a session cast onto a team, and again when the person stops", async () => {
		tokenState.fetch.mockResolvedValueOnce(listing("audit"));
		await mountMenu();
		await settle();
		expect(screen.queryByText("AUDIT")).not.toBeNull();

		const cast = token({ ...ALICE, jti: "cast" });
		tokenState.narrowed.set(cast, cast);
		tokenState.fetch.mockResolvedValueOnce(listing("directory"));
		await act(async () => {
			tokenState.token = cast;
			window.dispatchEvent(new Event("codefly:auth-changed"));
		});
		await settle();
		expect(screen.queryByText("AUDIT")).toBeNull();
		expect(screen.queryByText("DIRECTORY")).not.toBeNull();
		expect(tokenState.fetch).toHaveBeenCalledTimes(2);

		tokenState.fetch.mockResolvedValueOnce(listing("audit"));
		await act(async () => signIn({ ...ALICE, jti: "own-again" }));
		await settle();
		expect(screen.queryByText("DIRECTORY")).toBeNull();
		expect(screen.queryByText("AUDIT")).not.toBeNull();
		expect(tokenState.fetch).toHaveBeenCalledTimes(3);
	});
	// Red before: the cast's own bearer was the viewer, so the renewed bearer
	// dropped the list and every page mounted behind it remounted.
	it("keeps the cast's list across a renewal of the same cast", async () => {
		const cast = token({ ...ALICE, jti: "cast" });
		tokenState.narrowed.set(cast, cast);
		tokenState.token = cast;
		tokenState.fetch.mockResolvedValueOnce(listing("directory"));
		await mountMenu();
		await settle();
		expect(screen.queryByText("DIRECTORY")).not.toBeNull();

		const renewed = token({ ...ALICE, jti: "cast-renewed" });
		tokenState.narrowed.set(renewed, cast);
		await act(async () => {
			tokenState.token = renewed;
			window.dispatchEvent(new Event("codefly:auth-changed"));
		});
		await settle();
		expect(screen.queryByText("DIRECTORY")).not.toBeNull();
		expect(tokenState.fetch).toHaveBeenCalledTimes(1);
	});

	it("publishes a generation change even when navigation and availability are unchanged", async () => {
		vi.useFakeTimers();
		try {
			const { useRegisteredSolutions } = await import("../SolutionsMenu");
			function Version() {
				return <span>{useRegisteredSolutions()[0]?.assetVersion}</span>;
			}
			const answer = (assetVersion: string) =>
				Response.json({
					solutions: [
						{
							id: "audit",
							nav: { title: "Audit", path: "/s/audit" },
							available: true,
							assetVersion,
						},
					],
				});
			tokenState.fetch.mockResolvedValue(answer("old"));
			render(<Version />);
			await act(async () => {
				await vi.advanceTimersByTimeAsync(0);
			});
			expect(screen.queryByText("old")).not.toBeNull();
			tokenState.fetch.mockResolvedValue(answer("new"));
			await act(async () => {
				await vi.advanceTimersByTimeAsync(10_000);
			});
			expect(screen.queryByText("new")).not.toBeNull();
		} finally {
			vi.useRealTimers();
		}
	});
});

it("updates group headings and membership through the existing poll without changing links", async () => {
	vi.resetModules();
	tokenState.token = token(ALICE);
	tokenState.fetch.mockReset();
	vi.useFakeTimers();
	try {
		const answer = (id: string, title: string) =>
			Response.json({
				solutions: [
					{
						id: "audit",
						nav: { title: "Audit", path: "/s/audit" },
						available: true,
						group: { id, title, order: 1 },
					},
				],
			});
		tokenState.fetch.mockResolvedValue(answer("operations", "Operations"));
		await mountMenu();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(
			screen.queryByRole("heading", { name: "Operations" }),
		).not.toBeNull();
		expect(
			screen.getByRole("link", { name: "Audit" }).getAttribute("href"),
		).toBe("/s/audit");
		tokenState.fetch.mockResolvedValue(answer("review", "Review"));
		await act(async () => {
			await vi.advanceTimersByTimeAsync(10_000);
		});
		expect(screen.queryByRole("heading", { name: "Operations" })).toBeNull();
		expect(screen.queryByRole("heading", { name: "Review" })).not.toBeNull();
		expect(
			screen.getByRole("link", { name: "Audit" }).getAttribute("href"),
		).toBe("/s/audit");
		tokenState.fetch.mockResolvedValue(listing());
		await act(async () => {
			await vi.advanceTimersByTimeAsync(10_000);
		});
		expect(screen.queryByRole("heading", { name: "Review" })).toBeNull();
	} finally {
		cleanup();
		vi.useRealTimers();
	}
});
