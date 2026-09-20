// @vitest-environment happy-dom
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CardRoot, CardContent } from "../card-root.js";
import { TabsRoot, TabsList, TabsTrigger, TabsContent } from "../tabs-root.js";
import {
	Sheet,
	SheetContent,
	SheetHeader,
	SheetTitle,
	SheetDescription,
	SheetTrigger,
} from "../sheet.js";
import { SidebarProvider, SidebarTrigger, useSidebar } from "../sidebar.js";
import {
	InputGroup,
	InputGroupInput,
	InputGroupAddon,
} from "../input-group.js";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

it("forwards Card refs and DOM events through compound composition", () => {
	const ref = createRef<HTMLDivElement>();
	const click = vi.fn();
	render(
		<CardRoot ref={ref} onClick={click}>
			<CardContent>Content</CardContent>
		</CardRoot>,
	);
	fireEvent.click(screen.getByText("Content"));
	expect(click).toHaveBeenCalledOnce();
	expect(ref.current?.contains(screen.getByText("Content"))).toBe(true);
});

it("focuses the grouped input when its addon is clicked", () => {
	render(
		<InputGroup>
			<InputGroupAddon>Website</InputGroupAddon>
			<InputGroupInput aria-label="Website" />
		</InputGroup>,
	);
	fireEvent.click(screen.getByText("Website"));
	expect(document.activeElement).toBe(screen.getByRole("textbox"));
});

it("navigates vertical tabs without activating disabled items", async () => {
	render(
		<TabsRoot defaultValue="one" orientation="vertical">
			<TabsList activateOnFocus>
				<TabsTrigger value="one">One</TabsTrigger>
				<TabsTrigger value="disabled" disabled>
					Unavailable
				</TabsTrigger>
				<TabsTrigger value="two">Two</TabsTrigger>
			</TabsList>
			<TabsContent value="one">First</TabsContent>
			<TabsContent value="two">Second</TabsContent>
		</TabsRoot>,
	);
	const first = screen.getByRole("tab", { name: "One" });
	act(() => first.focus());
	fireEvent.keyDown(first, { key: "ArrowDown" });
	await waitFor(() =>
		expect(document.activeElement).toBe(
			screen.getByRole("tab", { name: "Unavailable" }),
		),
	);
	expect(first.getAttribute("aria-selected")).toBe("true");
	fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
	await waitFor(() =>
		expect(
			screen.getByRole("tab", { name: "Two" }).getAttribute("aria-selected"),
		).toBe("true"),
	);
	expect(document.activeElement).toBe(screen.getByRole("tab", { name: "Two" }));
	expect(screen.getByRole("tablist").getAttribute("aria-orientation")).toBe(
		"vertical",
	);
});

it("restores Sheet trigger focus after Escape", async () => {
	render(
		<Sheet>
			<SheetTrigger>Details</SheetTrigger>
			<SheetContent>
				<SheetHeader>
					<SheetTitle>Details panel</SheetTitle>
					<SheetDescription>Workspace details</SheetDescription>
				</SheetHeader>
				<button type="button">Inside</button>
			</SheetContent>
		</Sheet>,
	);
	const trigger = screen.getByRole("button", { name: "Details" });
	act(() => trigger.focus());
	fireEvent.click(trigger);
	await screen.findByRole("dialog");
	fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
	await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	await waitFor(() => expect(document.activeElement).toBe(trigger));
});

function SidebarState() {
	const { open } = useSidebar();
	return <span>{open ? "Expanded" : "Collapsed"}</span>;
}

describe("Sidebar state ownership", () => {
	it("updates uncontrolled state and reports intent without persisting a cookie", () => {
		const changed = vi.fn();
		const cookie = vi.spyOn(document, "cookie", "set");
		render(
			<SidebarProvider onOpenChange={changed}>
				<SidebarTrigger />
				<SidebarState />
			</SidebarProvider>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Toggle Sidebar" }));
		expect(screen.getByText("Collapsed")).toBeTruthy();
		expect(changed).toHaveBeenCalledWith(false);
		expect(cookie).not.toHaveBeenCalled();
	});
	it("keeps controlled state until the owner accepts a change", () => {
		const changed = vi.fn();
		const { rerender } = render(
			<SidebarProvider open onOpenChange={changed}>
				<SidebarTrigger />
				<SidebarState />
			</SidebarProvider>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Toggle Sidebar" }));
		expect(screen.getByText("Expanded")).toBeTruthy();
		expect(changed).toHaveBeenCalledWith(false);
		rerender(
			<SidebarProvider open={false} onOpenChange={changed}>
				<SidebarState />
			</SidebarProvider>,
		);
		expect(screen.getByText("Collapsed")).toBeTruthy();
	});
});
