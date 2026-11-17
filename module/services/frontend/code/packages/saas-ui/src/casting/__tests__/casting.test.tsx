import { Code, ConnectError } from "@connectrpc/connect";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	CastBar,
	CastingProvider,
	openTeamSwitcher,
	TeamSwitcher,
	ViewAsTeamButton,
} from "../casting.js";
import type { CastHost, CastSession, CastTeam } from "../host.js";
import { forgetCastTeams } from "../store.js";

const own: CastSession = {
	personId: "alice",
	personDisplayName: "Alice",
	organizationId: "acme",
	castTeamId: "",
	roleView: [],
};
const admins: CastTeam = {
	id: "dir-admin",
	name: "Directory admins",
	holds: [
		{
			installationId: "i1",
			target: "directory",
			title: "Directory",
			role: "admin",
		},
	],
};
const users: CastTeam = {
	id: "dir-user",
	name: "Directory users",
	holds: [
		{
			installationId: "i1",
			target: "directory",
			title: "Directory",
			role: "user",
		},
	],
};

/** A host the way both real ones behave: a cast lists only itself, a switch goes back first. */
function fakeHost(over: Partial<CastHost> = {}) {
	let session = own;
	const calls: string[] = [];
	const host: CastHost = {
		current: async () => session,
		listTeams: async () =>
			session.castTeamId
				? [admins, users].filter((t) => t.id === session.castTeamId)
				: [admins, users],
		open: vi.fn(async (team: CastTeam) => {
			calls.push(`open ${team.id}`);
			if (session.castTeamId)
				throw new ConnectError("already cast", Code.PermissionDenied);
			session = { ...own, castTeamId: team.id };
		}),
		back: vi.fn(async () => {
			calls.push("back");
			session = own;
		}),
		changed: vi.fn(),
		...over,
	};
	return {
		host,
		calls,
		cast: (id: string) => (session = { ...own, castTeamId: id }),
	};
}

const renderCasting = (host: CastHost) =>
	render(
		<CastingProvider host={host}>
			<CastBar />
			<TeamSwitcher />
			<ViewAsTeamButton team={users} />
		</CastingProvider>,
	);

/** Reads the person's own view once, so the tab keeps the list a cast switches from. */
async function readOwnView(host: CastHost) {
	const view = renderCasting(host);
	await waitFor(() =>
		expect(sessionStorage.getItem("codefly_cast_teams") ?? "").toContain(
			"dir-user",
		),
	);
	view.unmount();
}

beforeEach(() => forgetCastTeams());
afterEach(cleanup);

describe("team casting", () => {
	it("casts from the person's own view and says so in one announcement", async () => {
		const { host } = fakeHost();
		renderCasting(host);
		fireEvent.click(
			await screen.findByRole("button", { name: /View as this team/ }),
		);
		await waitFor(() =>
			expect(screen.getByRole("status").textContent).toContain(
				"Viewing as Directory users",
			),
		);
		// The live region is the announcement alone, never the controls beside it.
		expect(screen.getByRole("status").querySelector("button")).toBeNull();
		expect(host.changed).toHaveBeenCalledTimes(1);
	});

	it("switches from a cast by going back first, from the list kept in the person's own view", async () => {
		const { host, calls, cast } = fakeHost();
		await readOwnView(host);
		cast("dir-admin");
		renderCasting(host);
		await waitFor(() =>
			expect(screen.getByRole("status").textContent).toContain(
				"Directory admins",
			),
		);
		act(() => openTeamSwitcher());
		fireEvent.click(await screen.findByText("Directory users"));
		await waitFor(() =>
			expect(screen.getByRole("status").textContent).toContain(
				"Directory users",
			),
		);
		expect(calls).toEqual(["back", "open dir-user"]);
	});

	it("names a kept team's pages as the person's own menu named them, though the cast's menu lists none of them", async () => {
		// The host names a holding by its target; the menu, by its title. A
		// cast's menu lists only the cast's own pages. Red before: the
		// switcher, opened in a cast, headed the other teams' pages with their
		// ids ("records") rather than their titles ("Records").
		const records: CastTeam = {
			id: "rec-user",
			name: "records-user",
			holds: [
				{
					installationId: "i2",
					target: "records",
					title: "records",
					role: "user",
				},
			],
		};
		const { host, cast } = fakeHost({
			listTeams: async () => [admins, users, records],
		});
		const menu = new Map([
			["directory", "Directory"],
			["records", "Records"],
		]);
		const own = render(
			<CastingProvider host={host} titleOf={(target) => menu.get(target)}>
				<TeamSwitcher />
			</CastingProvider>,
		);
		await waitFor(() =>
			expect(sessionStorage.getItem("codefly_cast_teams") ?? "").toContain(
				"Records",
			),
		);
		own.unmount();
		cast("dir-admin");
		const castMenu = new Map([["directory", "Directory"]]);
		render(
			<CastingProvider host={host} titleOf={(target) => castMenu.get(target)}>
				<CastBar />
				<TeamSwitcher />
			</CastingProvider>,
		);
		await waitFor(() =>
			expect(screen.getByRole("status").textContent).toContain(
				"Directory admins",
			),
		);
		act(() => openTeamSwitcher());
		expect(await screen.findByText("Records")).toBeTruthy();
	});

	it("says a refusal after the way back in the host's words, and a failed way back as itself", async () => {
		const { host, cast } = fakeHost();
		await readOwnView(host);
		cast("dir-admin");
		host.open = async () => {
			throw new ConnectError(
				"team holds roles you could not grant",
				Code.PermissionDenied,
			);
		};
		renderCasting(host);
		await screen.findByRole("status");
		act(() => openTeamSwitcher());
		fireEvent.click(await screen.findByText("Directory users"));
		expect((await screen.findByRole("alert")).textContent).toBe(
			"You are back in your own view. Directory users was not opened: team holds roles you could not grant (PermissionDenied)",
		);

		fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		cleanup();
		cast("dir-admin");
		host.back = async () => {
			throw new TypeError("Failed to fetch");
		};
		renderCasting(host);
		await screen.findByRole("status");
		act(() => openTeamSwitcher());
		fireEvent.click(await screen.findByText("Directory users"));
		const alert = (await screen.findByRole("alert")).textContent ?? "";
		expect(alert).toBe(
			"Not viewing as Directory users: Could not go back to your own view: Failed to fetch",
		);
		expect(alert).not.toContain("Unknown");
	});

	it("forgets the kept list when the host says the session is signed out", async () => {
		const { host, cast } = fakeHost();
		await readOwnView(host);
		cast("dir-admin");
		host.current = async () => {
			throw new ConnectError("no session", Code.Unauthenticated);
		};
		renderCasting(host);
		await waitFor(() =>
			expect(sessionStorage.getItem("codefly_cast_teams")).toBeNull(),
		);
		expect(screen.queryByRole("status")).toBeNull();
	});

	it("returns focus to Switch team when the switcher closes", async () => {
		const { host, cast } = fakeHost();
		await readOwnView(host);
		cast("dir-admin");
		renderCasting(host);
		const switchTeam = await screen.findByRole("button", {
			name: "Switch team",
		});
		switchTeam.focus();
		fireEvent.click(switchTeam);
		fireEvent.keyDown(await screen.findByRole("dialog"), { key: "Escape" });
		await waitFor(() =>
			expect(document.activeElement).toBe(
				screen.getByRole("button", { name: "Switch team" }),
			),
		);
	});

	it("asks the host again once a cast is past the end it stated, and shows what it then answers", async () => {
		const { host, cast } = fakeHost();
		await readOwnView(host);
		const ending = {
			...cast("dir-admin"),
			expiresAtUnix: Math.floor(Date.now() / 1000) + 1,
		};
		let asked = 0;
		// The host answers the cast until its end, and the person's own after.
		host.current = async () =>
			++asked === 1
				? ending
				: { ...own, expiresAtUnix: Math.floor(Date.now() / 1000) + 3600 };
		renderCasting(host);
		await waitFor(() =>
			expect(screen.getByRole("status").textContent).toContain(
				"Directory admins",
			),
		);
		await waitFor(() => expect(screen.queryByRole("status")).toBeNull(), {
			timeout: 3000,
		});
		expect(asked).toBe(2);
	});

	it("does not present a session past the end the host stated, though the host answers it again", async () => {
		const { host, cast } = fakeHost();
		await readOwnView(host);
		const ended = {
			...cast("dir-admin"),
			expiresAtUnix: Math.floor(Date.now() / 1000) - 1,
		};
		let asked = 0;
		host.current = async () => {
			asked++;
			return ended;
		};
		renderCasting(host);
		await waitFor(() => expect(asked).toBe(2));
		await act(() => new Promise<void>((resolve) => setTimeout(resolve, 100)));
		expect(screen.queryByRole("status")).toBeNull();
		// Asked once more at its end, never in a loop.
		expect(asked).toBe(2);
	});

	it("keeps a session whose host states no end", async () => {
		const { host, cast } = fakeHost();
		await readOwnView(host);
		cast("dir-admin");
		const current = vi.fn(host.current);
		host.current = current;
		renderCasting(host);
		await waitFor(() =>
			expect(screen.getByRole("status").textContent).toContain(
				"Directory admins",
			),
		);
		await act(() => new Promise<void>((resolve) => setTimeout(resolve, 100)));
		expect(current).toHaveBeenCalledTimes(1);
	});
});
