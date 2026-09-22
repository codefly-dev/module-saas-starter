import { useQuery } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { StrictMode, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { Providers } from "../providers";

const state = vi.hoisted(() => ({
	auth: {
		user: { id: "user-a" } as { id: string } | null,
		organizationId: "org-a",
		orgRole: "admin",
		platformRole: "support",
		isAuthenticated: true,
		accessToken: "token-a",
		impersonation: {
			isImpersonating: false,
			impersonatorId: "",
			subjectId: "",
		},
	},
}));

vi.mock("../auth", () => ({
	AuthProvider: ({ children }: { children: ReactNode }) => children,
	useAuth: () => state.auth,
}));
vi.mock("../../../frontend.config", () => ({
	default: { appearance: { defaultTheme: "system" } },
}));
vi.mock("../plugins/runtime", () => ({ hostPluginRuntime: {} }));
vi.mock("@codefly-dev/ui/plugin-host/runtime", () => ({
	PluginRuntimeProvider: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("../theme-provider", () => ({
	ThemeProvider: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("../analytics/provider", () => ({
	AnalyticsProvider: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("../appearance-provider", () => ({
	AppearanceProvider: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("@/features/user-settings/ui/theme-preference-provider", () => ({
	ThemePreferenceProvider: ({ children }: { children: ReactNode }) => children,
}));

const initial = structuredClone(state.auth);
beforeEach(() => {
	state.auth = structuredClone(initial);
});
afterEach(cleanup);

function Probe({ read }: { read: () => Promise<string> }) {
	const { data } = useQuery({ queryKey: ["private-record"], queryFn: read });
	return <span>{data ?? "loading"}</span>;
}

function tree(read: () => Promise<string>) {
	return (
		<StrictMode>
			<Providers>
				<Probe read={read} />
			</Providers>
		</StrictMode>
	);
}

it.each([
	[
		"organization",
		() => {
			state.auth.organizationId = "org-b";
		},
	],
	[
		"user",
		() => {
			state.auth.user = { id: "user-b" };
		},
	],
	[
		"org demotion",
		() => {
			state.auth.orgRole = "member";
		},
	],
	[
		"platform demotion",
		() => {
			state.auth.platformRole = "";
		},
	],
	[
		"logout",
		() => {
			state.auth.user = null;
			state.auth.isAuthenticated = false;
		},
	],
	[
		"impersonation",
		() => {
			state.auth.impersonation = {
				isImpersonating: true,
				impersonatorId: "admin-a",
				subjectId: "user-b",
			};
		},
	],
] as const)("discards cached data on %s change", async (_, change) => {
	const read = vi.fn().mockResolvedValue("private before");
	const view = render(tree(read));
	await screen.findByText("private before");
	read.mockResolvedValue("private after");
	change();
	view.rerender(tree(read));
	expect(screen.queryByText("private before")).toBeNull();
	await screen.findByText("private after");
});

it("keeps cached data during same-authority token refresh", async () => {
	const read = vi.fn().mockResolvedValue("private before");
	const view = render(tree(read));
	await screen.findByText("private before");
	const calls = read.mock.calls.length;
	state.auth.accessToken = "refreshed-token";
	view.rerender(tree(read));
	expect(screen.getByText("private before")).toBeTruthy();
	expect(read).toHaveBeenCalledTimes(calls);
});

it("does not admit an old in-flight response into the new authority", async () => {
	let finish!: (value: string) => void;
	const pending = new Promise<string>((resolve) => {
		finish = resolve;
	});
	const read = vi.fn(() => pending);
	const view = render(tree(read));
	await waitFor(() => expect(read).toHaveBeenCalled());
	state.auth.organizationId = "org-b";
	read.mockImplementation(() => Promise.resolve("new organization"));
	view.rerender(tree(read));
	await screen.findByText("new organization");
	await act(async () => {
		finish("old organization");
		await pending;
	});
	expect(screen.queryByText("old organization")).toBeNull();
	expect(screen.getByText("new organization")).toBeTruthy();
});
