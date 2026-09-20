// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Badge, Button, Input, Separator, Skeleton } from "../index.js";
import * as layout from "../index.js";

afterEach(cleanup);

// The primitives were promoted out of the host `src/components/ui` into the kit as
// their single sealed home (issue #451). This guards the public surface of the
// `@codefly-dev/ui/layout` barrel so a rename or a dropped re-export is caught here
// rather than in a downstream build.
describe("@codefly-dev/ui/layout exports the promoted primitives", () => {
	for (const name of [
		// existing containers
		"Card",
		"Section",
		"Tabs",
		// feedback / state
		"EmptyState",
		"ErrorState",
		// actions
		"Button",
		"buttonVariants",
		// forms
		"Input",
		"Textarea",
		"Label",
		"Checkbox",
		"Switch",
		"Select",
		"SelectContent",
		"SelectItem",
		"SelectTrigger",
		"SelectValue",
		// data display
		"Badge",
		"badgeVariants",
		"Avatar",
		"AvatarFallback",
		"AvatarImage",
		"Table",
		"TableBody",
		"TableCell",
		"TableRow",
		"Skeleton",
		"Separator",
		// overlays
		"Dialog",
		"DialogContent",
		"DialogTitle",
		"DialogTrigger",
		"AlertDialog",
		"AlertDialogContent",
		"AlertDialogAction",
		"Tooltip",
		"TooltipContent",
		"TooltipTrigger",
		"DropdownMenu",
		"DropdownMenuContent",
		"DropdownMenuItem",
		"DropdownMenuTrigger",
	]) {
		it(`exports ${name}`, () => {
			expect((layout as Record<string, unknown>)[name]).toBeDefined();
		});
	}
});

// Smoke test the atoms that render a plain element, proving the promoted files
// resolve against the kit's own `cn` (clsx + tailwind-merge) and `cva` and paint a
// caller's className last.
describe("promoted atoms render", () => {
	it("Button renders its label and paints a caller's className last", () => {
		// `rounded-full` conflicts with the button's own `rounded-lg`. tailwind
		// -merge must let the caller win — the class is present AND the base class
		// it overrides is gone. A plain truthy-join would keep both `rounded-lg`
		// and `rounded-full` and leave CSS source-order to decide, so this asserts
		// the override actually resolved rather than merely that the class is
		// appended.
		render(
			<Button variant="outline" className="rounded-full custom-x">
				Save
			</Button>,
		);
		const button = screen.getByRole("button", { name: "Save" });
		expect(button.className).toContain("custom-x");
		expect(button.className).toContain("rounded-full");
		expect(button.className).not.toContain("rounded-lg");
	});

	it("Badge renders its content", () => {
		render(<Badge>New</Badge>);
		expect(screen.getByText("New")).toBeTruthy();
	});

	it("Input accepts a value and placeholder", () => {
		render(<Input placeholder="email" defaultValue="a@b.c" />);
		const input = screen.getByPlaceholderText("email") as HTMLInputElement;
		expect(input.value).toBe("a@b.c");
	});

	it("Skeleton and Separator render", () => {
		const { container } = render(
			<div>
				<Skeleton className="h-4 w-10" />
				<Separator />
			</div>,
		);
		expect(container.querySelector(".h-4")).toBeTruthy();
	});
});
