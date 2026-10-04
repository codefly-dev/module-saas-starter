import {
	act,
	cleanup,
	render,
	renderHook,
	screen,
	waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	type NameOf,
	type PrincipalNamesProviderProps,
	PrincipalName,
	PrincipalNamesProvider,
	useNameOf,
	usePrincipalDirectory,
	usePrincipalNames,
	viewerPrincipal,
} from "../index.js";

afterEach(cleanup);

function token(claims: Record<string, unknown>): string {
	return `header.${btoa(JSON.stringify(claims)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "")}.signature`;
}

function body(init?: RequestInit): unknown {
	return JSON.parse(new TextDecoder().decode(init?.body as Uint8Array));
}

const ALICE = "00000000-0000-7000-8000-0000000000a1";
const BOB = "00000000-0000-7000-8000-0000000000b2";
const CAROL = "00000000-0000-7000-8000-0000000000c3";
const STRANGER = "00000000-0000-7000-8000-0000000000ff";
const ORG_A = "00000000-0000-7000-8000-00000000000a";
const ORG_B = "00000000-0000-7000-8000-00000000000b";

const aliceToken = token({ iss: "host", sub: ALICE, org: ORG_A, auth_time: 1 });
const bobToken = token({ iss: "host", sub: BOB, org: ORG_B, auth_time: 2 });

function member(orgId: string, userId: string, userEmail: string) {
	return { orgId, userId, userEmail };
}

const orgA = [
	member(ORG_A, ALICE, "alice@example.com"),
	member(ORG_A, CAROL, "carol@example.com"),
];

/** A directory that answers every read with `members`, counting the reads. */
function directory(members: ReturnType<typeof member>[]) {
	return vi.fn<typeof fetch>(async () => Response.json({ members }));
}

function host(
	authedFetch: typeof fetch,
	getAccessToken: () => string | null = () => aliceToken,
): PrincipalNamesProviderProps["binding"] {
	return { apiBase: "/api", getAccessToken, authedFetch };
}

function wrapper(binding: PrincipalNamesProviderProps["binding"]) {
	return ({ children }: { children: ReactNode }) => (
		<PrincipalNamesProvider binding={binding}>
			{children}
		</PrincipalNamesProvider>
	);
}

/** The page's one directory state, as text. */
function Status() {
	const directory = usePrincipalDirectory();
	return (
		<p>
			{directory.status === "refused" || directory.status === "failed"
				? `${directory.status}: ${directory.reason}`
				: directory.status}
		</p>
	);
}

describe("viewerPrincipal", () => {
	it("is the token's subject, the real actor even when acting as someone", () => {
		expect(viewerPrincipal(aliceToken)).toBe(ALICE);
		expect(viewerPrincipal(token({ sub: ALICE, acting: BOB }))).toBe(ALICE);
		expect(viewerPrincipal(null)).toBe("");
		expect(viewerPrincipal("opaque")).toBe("");
	});
});

describe("usePrincipalNames", () => {
	it("reads the viewer's organization once for ten cards", async () => {
		const authedFetch = directory(orgA);
		render(
			<PrincipalNamesProvider binding={host(authedFetch)}>
				{Array.from({ length: 10 }, (_, card) => (
					<PrincipalName key={card} principal={CAROL} />
				))}
			</PrincipalNamesProvider>,
		);
		await waitFor(() =>
			expect(screen.getAllByText("carol@example.com")).toHaveLength(10),
		);
		expect(authedFetch).toHaveBeenCalledTimes(1);
		const [url, init] = authedFetch.mock.calls[0]!;
		expect(String(url)).toBe(
			"/api/saas.accounts.v1.DirectoryService/ListOrganizationMembers",
		);
		expect(body(init)).toEqual({ orgId: ORG_A });
		expect(new Headers(init?.headers).get("authorization")).toBe(
			`Bearer ${aliceToken}`,
		);
	});

	it("keeps the answer across re-renders and for components mounted later", async () => {
		const authedFetch = directory(orgA);
		const binding = host(authedFetch);
		const { result, rerender } = renderHook(
			({ principals }) => usePrincipalNames(principals),
			{ wrapper: wrapper(binding), initialProps: { principals: [CAROL] } },
		);
		await waitFor(() =>
			expect(result.current[CAROL]?.display).toBe("carol@example.com"),
		);
		const first = result.current;
		rerender({ principals: [CAROL] });
		expect(result.current).toBe(first);
		rerender({ principals: [CAROL, ALICE] });
		expect(result.current[ALICE]?.display).toBe("alice@example.com");

		render(
			<PrincipalNamesProvider binding={binding}>
				<PrincipalName principal={CAROL} />
			</PrincipalNamesProvider>,
		);
		await screen.findByText("carol@example.com");
		// A second provider is a second page; within the first, nothing re-read.
		expect(authedFetch).toHaveBeenCalledTimes(2);
		rerender({ principals: [CAROL] });
		expect(authedFetch).toHaveBeenCalledTimes(2);
	});

	it("marks only an exact match with the viewer as you", async () => {
		const near = ALICE.toUpperCase();
		const authedFetch = directory([
			...orgA,
			member(ORG_A, near, "alice-again@example.com"),
		]);
		const { result } = renderHook(
			() => usePrincipalNames([ALICE, near, CAROL]),
			{ wrapper: wrapper(host(authedFetch)) },
		);
		await waitFor(() => expect(result.current[ALICE]).toBeDefined());
		expect(result.current[ALICE]).toEqual({
			display: "alice@example.com",
			email: "alice@example.com",
			you: true,
		});
		expect(result.current[near]?.you).toBe(false);
		expect(result.current[CAROL]?.you).toBe(false);

		render(
			<PrincipalNamesProvider binding={host(authedFetch)}>
				<PrincipalName principal={ALICE} />
				<PrincipalName principal={near} />
			</PrincipalNamesProvider>,
		);
		expect(screen.getByText("You").getAttribute("title")).toBe(ALICE);
		await screen.findByText("alice-again@example.com");
		expect(screen.getAllByText("You")).toHaveLength(1);
	});

	it("never names a principal outside the viewer's organization", async () => {
		// Even a row the directory should never have sent, naming another
		// organization, is not taken as a name.
		const authedFetch = directory([
			...orgA,
			member(ORG_B, STRANGER, "stranger@example.com"),
			member(ORG_A, BOB, ""),
		]);
		const { result } = renderHook(
			() => usePrincipalNames([CAROL, STRANGER, BOB, "nobody"]),
			{ wrapper: wrapper(host(authedFetch)) },
		);
		await waitFor(() => expect(result.current[CAROL]).toBeDefined());
		expect(result.current[STRANGER]).toBeUndefined();
		expect(result.current[BOB]).toBeUndefined();
		expect(result.current.nobody).toBeUndefined();
	});

	it("shows an unknown principal as its shortened id, with the full id in the title", async () => {
		const authedFetch = directory(orgA);
		render(
			<PrincipalNamesProvider binding={host(authedFetch)}>
				<PrincipalName principal={STRANGER} />
				<PrincipalName principal={CAROL} />
			</PrincipalNamesProvider>,
		);
		const loading = screen.getByTitle(STRANGER);
		expect(loading.textContent).toBe("0000…00ff");
		expect(loading.getAttribute("data-state")).toBe("loading");
		expect(loading.getAttribute("aria-busy")).toBe("true");

		await screen.findByText("carol@example.com");
		const unknown = screen.getByTitle(STRANGER);
		expect(unknown.textContent).toBe("0000…00ff");
		expect(unknown.getAttribute("data-state")).toBe("unknown");
		expect(unknown.getAttribute("aria-busy")).toBeNull();
		expect(screen.getByTitle(CAROL).getAttribute("data-state")).toBe("named");
	});

	it("leaves every name undefined when the directory is refused, and says so once", async () => {
		const authedFetch = vi.fn<typeof fetch>(async () =>
			Response.json(
				{ code: "permission_denied", message: "not a member" },
				{ status: 403 },
			),
		);
		const { result } = renderHook(
			() => ({
				names: usePrincipalNames([ALICE, CAROL]),
				directory: usePrincipalDirectory(),
				nameOf: useNameOf(),
			}),
			{ wrapper: wrapper(host(authedFetch)) },
		);
		expect(result.current.directory).toEqual({ status: "loading" });
		await waitFor(() =>
			expect(result.current.directory).toEqual({
				status: "refused",
				reason: "not a member",
			}),
		);
		expect(result.current.names[ALICE]).toBeUndefined();
		expect(result.current.names[CAROL]).toBeUndefined();
		expect(result.current.nameOf(CAROL)).toBeUndefined();

		render(
			<PrincipalNamesProvider binding={host(authedFetch)}>
				<PrincipalName principal={CAROL} />
				<PrincipalName principal={ALICE} />
			</PrincipalNamesProvider>,
		);
		await waitFor(() =>
			expect(screen.getByTitle(CAROL).getAttribute("data-state")).toBe(
				"refused",
			),
		);
		expect(screen.getByTitle(CAROL).textContent).toBe("0000…00c3");
		// The viewer still knows who they are.
		expect(screen.getByTitle(ALICE).textContent).toBe("You");
	});

	it("refuses without asking when the credential names no organization", async () => {
		const authedFetch = directory(orgA);
		const { result } = renderHook(() => usePrincipalDirectory(), {
			wrapper: wrapper(host(authedFetch, () => token({ sub: ALICE }))),
		});
		await waitFor(() => expect(result.current.status).toBe("refused"));
		expect(authedFetch).not.toHaveBeenCalled();
	});

	it("keeps a transient failure through its backoff, however many components ask", async () => {
		let reply = () =>
			Response.json({ code: "unavailable", message: "down" }, { status: 503 });
		const authedFetch = vi.fn<typeof fetch>(async () => reply());
		const binding = host(authedFetch);
		const page = (later: boolean) => (
			<PrincipalNamesProvider binding={binding}>
				<Status />
				{later && <PrincipalName principal={CAROL} />}
			</PrincipalNamesProvider>
		);
		const { rerender } = render(page(false));
		await screen.findByText(/^failed: /);
		reply = () => Response.json({ members: orgA });
		rerender(page(true));
		await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
		expect(screen.getByTitle(CAROL).getAttribute("data-state")).toBe("failed");
		expect(authedFetch).toHaveBeenCalledTimes(1);
	});

	it("keeps a refusal for the viewer rather than asking again", async () => {
		const authedFetch = vi.fn<typeof fetch>(async () =>
			Response.json({ code: "permission_denied" }, { status: 403 }),
		);
		const binding = host(authedFetch);
		const page = (later: boolean) => (
			<PrincipalNamesProvider binding={binding}>
				<Status />
				{later && <PrincipalName principal={CAROL} />}
			</PrincipalNamesProvider>
		);
		const { rerender } = render(page(false));
		await screen.findByText(/^refused: /);
		rerender(page(true));
		expect(screen.getByTitle(CAROL).getAttribute("data-state")).toBe("refused");
		expect(authedFetch).toHaveBeenCalledTimes(1);
	});

	it("never shows one viewer's names to the next, not even for one render", async () => {
		let current = aliceToken;
		const pending: ((reply: Response) => void)[] = [];
		const authedFetch = vi.fn<typeof fetch>(
			() => new Promise<Response>((resolve) => pending.push(resolve)),
		);
		const seen: { viewer: string; carol?: string }[] = [];
		function Names() {
			const names = usePrincipalNames([CAROL]);
			seen.push({ viewer: current, carol: names[CAROL]?.display });
			return null;
		}
		render(
			<PrincipalNamesProvider binding={host(authedFetch, () => current)}>
				<Names />
			</PrincipalNamesProvider>,
		);
		await waitFor(() => expect(pending).toHaveLength(1));
		await act(async () => {
			pending[0]!(Response.json({ members: orgA }));
		});
		expect(seen.at(-1)?.carol).toBe("carol@example.com");

		current = bobToken;
		await act(async () => {
			window.dispatchEvent(new Event("codefly:auth-changed"));
		});
		await waitFor(() => expect(pending).toHaveLength(2));
		expect(body(authedFetch.mock.calls[1]![1])).toEqual({ orgId: ORG_B });
		const bobs = seen.filter((render) => render.viewer === bobToken);
		expect(bobs.length).toBeGreaterThan(0);
		expect(bobs.filter((render) => render.carol)).toEqual([]);
	});

	it("drops an answer that arrives after the viewer changed", async () => {
		let current = aliceToken;
		const pending: ((reply: Response) => void)[] = [];
		const authedFetch = vi.fn<typeof fetch>(
			() => new Promise<Response>((resolve) => pending.push(resolve)),
		);
		const binding = host(authedFetch, () => current);
		function Names() {
			const nameOf: NameOf = useNameOf();
			const names = usePrincipalNames([CAROL, BOB]);
			return (
				<>
					<span data-testid="names">
						{`${names[CAROL]?.display ?? "-"} ${names[BOB]?.display ?? "-"} ${names[BOB]?.you}`}
					</span>
					<span data-testid="name-of">
						{`${nameOf(CAROL) ?? "-"} ${nameOf(BOB) ?? "-"}`}
					</span>
				</>
			);
		}
		const page = (later: boolean) => (
			<PrincipalNamesProvider binding={binding}>
				<Names />
				{later && <PrincipalName principal={CAROL} />}
			</PrincipalNamesProvider>
		);
		const { rerender } = render(page(false));
		await waitFor(() => expect(pending).toHaveLength(1));

		current = bobToken;
		await act(async () => {
			window.dispatchEvent(new Event("codefly:auth-changed"));
		});
		await waitFor(() => expect(pending).toHaveLength(2));

		// Alice's answer arrives after the switch: it reaches neither Bob's page
		// nor displaces Bob's read still in flight.
		await act(async () => {
			pending[0]!(Response.json({ members: orgA }));
		});
		expect(screen.getByTestId("names").textContent).toBe("- - undefined");
		expect(screen.getByTestId("name-of").textContent).toBe("- -");
		rerender(page(true));
		expect(authedFetch).toHaveBeenCalledTimes(2);

		await act(async () => {
			pending[1]!(
				Response.json({ members: [member(ORG_B, BOB, "bob@example.com")] }),
			);
		});
		expect(screen.getByTestId("names").textContent).toBe(
			"- bob@example.com true",
		);
		expect(screen.getByTestId("name-of").textContent).toBe("- bob@example.com");
		expect(screen.getByTitle(CAROL).getAttribute("data-state")).toBe("unknown");
	});

	describe("a page that renders names only once the directory settled", () => {
		function Settled() {
			const directory = usePrincipalDirectory();
			return directory.status === "loading" ? (
				<p>spinner</p>
			) : (
				<PrincipalName principal={CAROL} />
			);
		}

		/** A directory that fails after `latency`, refusing to be called past 50 times. */
		function failing(reply: () => Response, latency = 10) {
			return vi.fn<typeof fetch>(async () => {
				if (authedFetchCalls() > 50) throw new Error("runaway retry");
				await new Promise((resolve) => setTimeout(resolve, latency));
				return reply();
			});
		}
		let authedFetchCalls = () => 0;

		it.each([
			["a gateway that keeps answering 502", 502, "unavailable", 2],
			["a malformed organization claim", 400, "invalid_argument", 1],
			["a missing proxy route", 404, "not_found", 1],
			["an unimplemented procedure", 501, "unimplemented", 1],
			["a failed precondition", 400, "failed_precondition", 1],
		])(
			"makes a bounded number of reads against %s",
			async (_, status, code, afterBackoff) => {
				const authedFetch = failing(() =>
					Response.json({ code, message: "no" }, { status }),
				);
				authedFetchCalls = () => authedFetch.mock.calls.length;
				const binding = host(authedFetch);
				const page = (extra: boolean) => (
					<PrincipalNamesProvider binding={binding}>
						<Settled />
						{extra && <PrincipalName principal={ALICE} />}
					</PrincipalNamesProvider>
				);
				const { rerender } = render(page(false));
				await waitFor(() =>
					expect(screen.getByTitle(CAROL).getAttribute("data-state")).toBe(
						"failed",
					),
				);
				await act(() => new Promise((resolve) => setTimeout(resolve, 300)));
				expect(authedFetch).toHaveBeenCalledTimes(1);
				expect(screen.queryByText("spinner")).toBeNull();

				// Past the longest backoff, a newly mounted name asks again only
				// when the failure could have healed by itself.
				const clock = vi
					.spyOn(Date, "now")
					.mockReturnValue(Date.now() + 120_000);
				try {
					rerender(page(true));
					await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
					expect(authedFetch).toHaveBeenCalledTimes(afterBackoff);
					expect(screen.queryByText("spinner")).toBeNull();
				} finally {
					clock.mockRestore();
				}
			},
		);

		it("retries a transient failure after a backoff without going back to loading", async () => {
			let reply = () =>
				Response.json(
					{ code: "unavailable", message: "down" },
					{ status: 503 },
				);
			const authedFetch = vi.fn<typeof fetch>(async () => reply());
			const binding = host(authedFetch);
			const page = (extra: boolean) => (
				<PrincipalNamesProvider binding={binding}>
					<Settled />
					{extra && <PrincipalName principal={ALICE} />}
				</PrincipalNamesProvider>
			);
			const { rerender } = render(page(false));
			await screen.findByTitle(CAROL);
			const now = Date.now();
			const clock = vi.spyOn(Date, "now").mockReturnValue(now + 60_000);
			try {
				reply = () => Response.json({ members: orgA });
				rerender(page(true));
				// The retry is in flight: the page keeps what it showed.
				expect(screen.queryByText("spinner")).toBeNull();
				await screen.findByText("carol@example.com");
				expect(authedFetch).toHaveBeenCalledTimes(2);
			} finally {
				clock.mockRestore();
			}
		});
	});

	it("drops an answer that arrives after the token changed but before the kit noticed", async () => {
		let current = aliceToken;
		const pending: ((reply: Response) => void)[] = [];
		const authedFetch = vi.fn<typeof fetch>(
			() => new Promise<Response>((resolve) => pending.push(resolve)),
		);
		const seen: { viewer: string; carol?: string }[] = [];
		function Names() {
			const names = usePrincipalNames([CAROL]);
			seen.push({ viewer: current, carol: names[CAROL]?.display });
			return null;
		}
		// No subscribeToken and no event: the kit learns of the switch only on
		// its next poll.
		render(
			<PrincipalNamesProvider binding={host(authedFetch, () => current)}>
				<Names />
			</PrincipalNamesProvider>,
		);
		await waitFor(() => expect(pending).toHaveLength(1));
		current = bobToken;
		await act(async () => {
			pending[0]!(Response.json({ members: orgA }));
		});
		expect(seen.filter((render) => render.carol)).toEqual([]);
	});

	it("treats an unauthenticated answer as recoverable on the next token", async () => {
		let current = aliceToken;
		let reply = () =>
			Response.json(
				{ code: "unauthenticated", message: "expired" },
				{ status: 401 },
			);
		const authedFetch = vi.fn<typeof fetch>(async () => reply());
		const binding = host(authedFetch, () => current);
		const page = (extra: boolean) => (
			<PrincipalNamesProvider binding={binding}>
				<Status />
				<PrincipalName principal={CAROL} />
				{extra && <PrincipalName principal={BOB} />}
			</PrincipalNamesProvider>
		);
		const { rerender } = render(page(false));
		await screen.findByText(/^failed: /);
		rerender(page(true));
		expect(authedFetch).toHaveBeenCalledTimes(1);

		// The host refreshes: same viewer (auth_time kept), a new token.
		current = token({
			iss: "host",
			sub: ALICE,
			org: ORG_A,
			auth_time: 1,
			sid: "s2",
		});
		reply = () => Response.json({ members: orgA });
		await act(async () => {
			window.dispatchEvent(new Event("codefly:auth-changed"));
		});
		await screen.findByText("carol@example.com");
		expect(screen.getByText("ready")).toBeTruthy();
		expect(authedFetch).toHaveBeenCalledTimes(2);
	});

	it("says a signed-out viewer has no credential instead of loading forever", () => {
		const authedFetch = directory(orgA);
		render(
			<PrincipalNamesProvider binding={host(authedFetch, () => null)}>
				<Status />
				<PrincipalName principal={CAROL} />
			</PrincipalNamesProvider>,
		);
		expect(screen.getByText("refused: no credential")).toBeTruthy();
		const name = screen.getByTitle(CAROL);
		expect(name.getAttribute("data-state")).toBe("refused");
		expect(name.getAttribute("aria-busy")).toBeNull();
		expect(authedFetch).not.toHaveBeenCalled();
	});

	it("gives an empty principal a state and a title", () => {
		render(
			<PrincipalNamesProvider binding={host(directory(orgA))}>
				<PrincipalName principal="" />
			</PrincipalNamesProvider>,
		);
		const none = screen.getByText("someone");
		expect(none.getAttribute("data-state")).toBe("none");
		expect(none.getAttribute("title")).toBe("No principal is recorded");
	});

	it("needs a provider", () => {
		vi.spyOn(console, "error").mockImplementation(() => {});
		expect(() => renderHook(() => usePrincipalNames([ALICE]))).toThrow(
			/PrincipalNamesProvider/,
		);
		vi.restoreAllMocks();
	});
});
