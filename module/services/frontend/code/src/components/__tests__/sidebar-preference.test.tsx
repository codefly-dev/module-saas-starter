import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
} from "@testing-library/react";
import { hydrateRoot } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, expect, it, vi } from "vitest";
import {
	SidebarProvider,
	SidebarTrigger,
	useSidebar,
} from "@/components/ui/sidebar";

function State() {
	const { open, openMobile } = useSidebar();
	return (
		<output>
			{open ? "Expanded" : "Collapsed"}
			{openMobile ? " mobile open" : ""}
		</output>
	);
}

function Content() {
	return (
		<>
			<SidebarTrigger />
			<State />
		</>
	);
}

afterEach(() => {
	cleanup();
	document.cookie = "sidebar_state=; path=/; max-age=0";
	vi.restoreAllMocks();
});

it("restores the user's collapsed choice on a fresh mount, then remembers expansion", () => {
	const first = render(
		<SidebarProvider>
			<Content />
		</SidebarProvider>,
	);
	fireEvent.click(screen.getByRole("button", { name: "Toggle Sidebar" }));
	expect(screen.getByText("Collapsed")).toBeTruthy();
	first.unmount();
	const changed = vi.fn();
	const cookieWrite = vi.spyOn(document, "cookie", "set");
	const second = render(
		<SidebarProvider onOpenChange={changed}>
			<Content />
		</SidebarProvider>,
	);
	expect(screen.getByText("Collapsed")).toBeTruthy();
	expect(changed).not.toHaveBeenCalled();
	expect(cookieWrite).not.toHaveBeenCalled();
	fireEvent.click(screen.getByRole("button", { name: "Toggle Sidebar" }));
	expect(changed).toHaveBeenCalledWith(true);
	second.unmount();
	render(
		<SidebarProvider>
			<Content />
		</SidebarProvider>,
	);
	expect(screen.getByText("Expanded")).toBeTruthy();
});

it.each(["", "invalid", "FALSE", "false-extra"])(
	"ignores an absent or malformed preference (%s)",
	(value) => {
		if (value) document.cookie = `sidebar_state=${value}; path=/`;
		render(
			<SidebarProvider>
				<Content />
			</SidebarProvider>,
		);
		expect(screen.getByText("Expanded")).toBeTruthy();
	},
);

it.each([true, false])(
	"honors an explicit initial default (%s) instead of an old browser preference",
	(defaultOpen) => {
		document.cookie = `sidebar_state=${!defaultOpen}; path=/`;
		render(
			<SidebarProvider defaultOpen={defaultOpen}>
				<Content />
			</SidebarProvider>,
		);
		expect(
			screen.getByText(defaultOpen ? "Expanded" : "Collapsed"),
		).toBeTruthy();
		fireEvent.click(screen.getByRole("button", { name: "Toggle Sidebar" }));
		expect(
			screen.getByText(defaultOpen ? "Collapsed" : "Expanded"),
		).toBeTruthy();
	},
);

it("leaves controlled state with its caller and reports toggle intent", () => {
	document.cookie = "sidebar_state=false; path=/";
	const changed = vi.fn();
	const view = render(
		<SidebarProvider open onOpenChange={changed}>
			<Content />
		</SidebarProvider>,
	);
	expect(screen.getByText("Expanded")).toBeTruthy();
	fireEvent.click(screen.getByRole("button", { name: "Toggle Sidebar" }));
	expect(changed).toHaveBeenCalledWith(false);
	expect(changed).toHaveBeenCalledTimes(1);
	expect(screen.getByText("Expanded")).toBeTruthy();
	view.rerender(
		<SidebarProvider open={false} onOpenChange={changed}>
			<Content />
		</SidebarProvider>,
	);
	expect(screen.getByText("Collapsed")).toBeTruthy();
});

it("keeps mobile drawer toggling separate from the remembered desktop choice", () => {
	document.cookie = "sidebar_state=false; path=/";
	const media = vi.spyOn(window, "matchMedia").mockImplementation((query) => ({
		matches: query === "(max-width: 767px)",
		media: query,
		onchange: null,
		addEventListener: vi.fn(),
		removeEventListener: vi.fn(),
		addListener: vi.fn(),
		removeListener: vi.fn(),
		dispatchEvent: vi.fn(),
	}));
	const changed = vi.fn();
	const view = render(
		<SidebarProvider onOpenChange={changed}>
			<Content />
		</SidebarProvider>,
	);
	fireEvent.click(screen.getByRole("button", { name: "Toggle Sidebar" }));
	expect(screen.getByText("Collapsed mobile open")).toBeTruthy();
	fireEvent.click(screen.getByRole("button", { name: "Toggle Sidebar" }));
	expect(screen.getByText("Collapsed")).toBeTruthy();
	expect(changed).not.toHaveBeenCalled();
	expect(document.cookie).toContain("sidebar_state=false");
	view.unmount();
	media.mockRestore();
	render(
		<SidebarProvider>
			<Content />
		</SidebarProvider>,
	);
	expect(screen.getByText("Collapsed")).toBeTruthy();
});

it("does not reinitialize remembered state when a controlled override is removed", () => {
	document.cookie = "sidebar_state=false; path=/";
	const view = render(
		<SidebarProvider open>
			<Content />
		</SidebarProvider>,
	);
	view.rerender(
		<SidebarProvider>
			<Content />
		</SidebarProvider>,
	);
	expect(screen.getByText("Expanded")).toBeTruthy();
	fireEvent.click(screen.getByRole("button", { name: "Toggle Sidebar" }));
	expect(screen.getByText("Collapsed")).toBeTruthy();
});

it("treats defaultOpen as an initial value when the caller changes it", () => {
	const view = render(
		<SidebarProvider defaultOpen={false}>
			<Content />
		</SidebarProvider>,
	);
	view.rerender(
		<SidebarProvider defaultOpen>
			<Content />
		</SidebarProvider>,
	);
	expect(screen.getByText("Collapsed")).toBeTruthy();
	view.rerender(
		<SidebarProvider>
			<Content />
		</SidebarProvider>,
	);
	expect(screen.getByText("Collapsed")).toBeTruthy();
});

it("hydrates a remembered choice without a recoverable markup mismatch", async () => {
	document.cookie = "sidebar_state=false; path=/";
	const content = (
		<SidebarProvider>
			<Content />
		</SidebarProvider>
	);
	const container = document.createElement("div");
	container.innerHTML = renderToString(content);
	document.body.append(container);
	const onRecoverableError = vi.fn();
	let root: ReturnType<typeof hydrateRoot>;
	await act(async () => {
		root = hydrateRoot(container, content, { onRecoverableError });
	});
	expect(container.textContent).toContain("Collapsed");
	expect(onRecoverableError).not.toHaveBeenCalled();
	await act(async () => root.unmount());
	container.remove();
});
