// @vitest-environment happy-dom
import { existsSync } from "node:fs";
import { join, resolve } from "node:path";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import ts from "typescript";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogFooter,
	AlertDialogTitle,
} from "../alert-dialog.js";
import { Button } from "../button.js";
import { CommandDialog } from "../command.js";
import { Dialog, DialogClose, DialogContent, DialogTitle } from "../dialog.js";
import { Notice } from "../notice.js";
import { Sheet, SheetClose, SheetContent, SheetTitle } from "../sheet.js";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

// A surface the user cannot leave must not be expressible with the kit. These
// tests hold both halves of that: at runtime, the way out is rendered and
// works; at compile time, a caller cannot take it away without naming another.

describe("DialogContent and SheetContent always render a way out", () => {
	it("renders the kit's close button by default, and it closes the dialog", async () => {
		render(
			<Dialog defaultOpen>
				<DialogContent>
					<DialogTitle>Edit</DialogTitle>
				</DialogContent>
			</Dialog>,
		);
		fireEvent.click(await screen.findByRole("button", { name: "Close" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("renders the caller's escape in place of the close button, and it works", async () => {
		render(
			<Dialog defaultOpen>
				<DialogContent
					showCloseButton={false}
					escape={<DialogClose render={<Button />}>Leave</DialogClose>}
				>
					<DialogTitle>Blocking step</DialogTitle>
				</DialogContent>
			</Dialog>,
		);
		const dialog = await screen.findByRole("dialog");
		expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
		const way = screen.getByRole("button", { name: "Leave" });
		// Inside the popup — so inside its focus trap and tab order.
		expect(dialog.contains(way)).toBe(true);
		expect(way.closest('[data-slot="dialog-escape"]')).not.toBeNull();
		fireEvent.click(way);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("renders a sheet's escape inside the sheet, and it works", async () => {
		render(
			<Sheet defaultOpen>
				<SheetContent
					showCloseButton={false}
					escape={<SheetClose render={<Button />}>Done</SheetClose>}
				>
					<SheetTitle>Details</SheetTitle>
				</SheetContent>
			</Sheet>,
		);
		const sheet = await screen.findByRole("dialog");
		const way = screen.getByRole("button", { name: "Done" });
		expect(sheet.contains(way)).toBe(true);
		fireEvent.click(way);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("gives the command palette the kit's close button by default", async () => {
		render(
			<CommandDialog defaultOpen>
				<p>Commands</p>
			</CommandDialog>,
		);
		fireEvent.click(await screen.findByRole("button", { name: "Close" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});
});

describe("AlertDialog offers a way to decline", () => {
	it("closes from its Cancel and from the Escape key", async () => {
		const action = vi.fn();
		const { rerender } = render(
			<AlertDialog defaultOpen>
				<AlertDialogContent>
					<AlertDialogTitle>Delete?</AlertDialogTitle>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={action}>Delete</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>,
		);
		fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(action).not.toHaveBeenCalled();

		rerender(
			<AlertDialog key="again" defaultOpen>
				<AlertDialogContent>
					<AlertDialogTitle>Delete?</AlertDialogTitle>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>,
		);
		await screen.findByRole("alertdialog");
		fireEvent.keyDown(document.activeElement ?? document.body, {
			key: "Escape",
		});
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});
});

describe("Notice", () => {
	it("is a labelled non-modal dialog whose escape is an enabled, focusable button", async () => {
		const onSelect = vi.fn();
		render(
			<Notice
				title="Review the terms"
				escape={{ label: "Sign out", onSelect }}
				actions={<Button disabled>Accept</Button>}
			>
				Accepting is unavailable right now.
			</Notice>,
		);
		const notice = screen.getByRole("dialog", { name: "Review the terms" });
		expect(notice.getAttribute("aria-modal")).toBe("false");
		expect(notice.getAttribute("aria-describedby")).toBeTruthy();

		const way = screen.getByRole("button", { name: "Sign out" });
		expect(notice.contains(way)).toBe(true);
		expect(way.hasAttribute("disabled")).toBe(false);
		expect(way.tabIndex).toBeGreaterThanOrEqual(0);
		way.focus();
		expect(document.activeElement).toBe(way);

		fireEvent.click(way);
		expect(onSelect).toHaveBeenCalledTimes(1);
	});

	it("comes before the notice's own actions, so a disabled decision never hides it", () => {
		render(
			<Notice
				title="Review the terms"
				escape={{ label: "Sign out", onSelect: () => {} }}
				actions={<Button disabled>Accept</Button>}
			/>,
		);
		const buttons = screen.getAllByRole("button");
		expect(buttons.map((b) => b.textContent)).toEqual(["Sign out", "Accept"]);
	});

	it("renders a dismiss escape as a labelled icon control", () => {
		const onSelect = vi.fn();
		render(
			<Notice
				title="Your privacy choices"
				escape={{ label: "Decide later", presentation: "dismiss", onSelect }}
			/>,
		);
		const way = screen.getByRole("button", { name: "Decide later" });
		expect(way.getAttribute("data-slot")).toBe("notice-escape");
		fireEvent.click(way);
		expect(onSelect).toHaveBeenCalledTimes(1);
	});

	it("floats by default and stays in flow inline", () => {
		const { rerender } = render(
			<Notice title="T" escape={{ label: "Out", onSelect: () => {} }} />,
		);
		expect(screen.getByRole("dialog").className).toContain("fixed");
		rerender(
			<Notice
				title="T"
				placement="inline"
				escape={{ label: "Out", onSelect: () => {} }}
			/>,
		);
		expect(screen.getByRole("dialog").className).not.toContain("fixed");
	});
});

// The type-level half. The `@ts-expect-error` lines are checked by the frontend
// typecheck (`tsc --noEmit` over the whole tree, this file included); if one of
// them ever compiles, that typecheck fails. The elements are created, never
// rendered.
describe("the types refuse a surface without a way out", () => {
	it("refuses them in this file (checked by tsc)", () => {
		const hide = Math.random() > 2;
		const refused = [
			// @ts-expect-error — hiding the close button requires an escape.
			<DialogContent key="a" showCloseButton={false} />,
			// @ts-expect-error — a computed boolean can be false, so it needs one too.
			<DialogContent key="b" showCloseButton={hide} />,
			// @ts-expect-error — null is not a way out.
			<DialogContent key="c" showCloseButton={false} escape={null} />,
			// @ts-expect-error — the same holds for a sheet.
			<SheetContent key="d" showCloseButton={false} />,
			// @ts-expect-error — and for the command palette.
			<CommandDialog key="e" showCloseButton={false}>
				x
			</CommandDialog>,
			// @ts-expect-error — a Notice cannot be written without an escape.
			<Notice key="f" title="T" />,
		];
		expect(refused).toHaveLength(6);
	});

	// The same refusal, observed by the compiler from inside this run, so it
	// cannot be skipped by a typecheck that does not include test files.
	it("reports a diagnostic for each refused fixture and none for the accepted one", {
		timeout: 60_000,
	}, () => {
		const layoutDir = kitLayoutDir();
		const header = `import { CommandDialog } from "../command.js";
import { DialogClose, DialogContent } from "../dialog.js";
import { Notice } from "../notice.js";
import { SheetContent } from "../sheet.js";
declare const flag: boolean;
void [CommandDialog, DialogClose, DialogContent, Notice, SheetContent, flag];
`;
		const fixtures: Record<string, string> = {
			hidden: `export const x = <DialogContent showCloseButton={false} />;`,
			computed: `export const x = <DialogContent showCloseButton={flag} />;`,
			nullEscape: `export const x = <DialogContent showCloseButton={false} escape={null} />;`,
			sheet: `export const x = <SheetContent showCloseButton={false} />;`,
			palette: `export const x = <CommandDialog showCloseButton={false}>x</CommandDialog>;`,
			notice: `export const x = <Notice title="T" />;`,
			disabledNoticeEscape: `export const x = <Notice title="T" escape={{ label: "Out", onSelect: () => {}, disabled: true }} />;`,
			accepted: `export const x = [
	<DialogContent key="a" />,
	<DialogContent key="b" showCloseButton />,
	<DialogContent key="c" showCloseButton={false} escape={<DialogClose>Out</DialogClose>} />,
	<Notice key="d" title="T" escape={{ label: "Out", onSelect: () => {} }} />,
];`,
		};
		const files = new Map(
			Object.entries(fixtures).map(([name, body]) => [
				join(layoutDir, "__tests__", `__escape_${name}__.tsx`),
				header + body,
			]),
		);
		const options: ts.CompilerOptions = {
			target: ts.ScriptTarget.ES2022,
			module: ts.ModuleKind.ESNext,
			moduleResolution: ts.ModuleResolutionKind.Bundler,
			jsx: ts.JsxEmit.ReactJSX,
			strict: true,
			skipLibCheck: true,
			noEmit: true,
			lib: ["lib.es2022.d.ts", "lib.dom.d.ts", "lib.dom.iterable.d.ts"],
		};
		const host = ts.createCompilerHost(options, true);
		const readFile = host.readFile.bind(host);
		const fileExists = host.fileExists.bind(host);
		const getSourceFile = host.getSourceFile.bind(host);
		host.readFile = (name) => files.get(resolve(name)) ?? readFile(name);
		host.fileExists = (name) => files.has(resolve(name)) || fileExists(name);
		host.getSourceFile = (name, version, onError, create) => {
			const text = files.get(resolve(name));
			return text === undefined
				? getSourceFile(name, version, onError, create)
				: ts.createSourceFile(name, text, version, true, ts.ScriptKind.TSX);
		};
		const program = ts.createProgram([...files.keys()], options, host);
		const errors = (name: string) =>
			program
				.getSemanticDiagnostics(
					program.getSourceFile(
						join(layoutDir, "__tests__", `__escape_${name}__.tsx`),
					),
				)
				.map((d) => ts.flattenDiagnosticMessageText(d.messageText, "\n"));

		expect(errors("accepted")).toEqual([]);
		// Each refusal is for the reason under test, not an unrelated error
		// (an unresolved import would fail every fixture, and "accepted" too).
		const refusedFor: Record<string, RegExp> = {
			hidden: /Property 'escape' is missing/,
			computed: /Property 'escape' is missing/,
			nullEscape: /Type 'null' is not assignable/,
			sheet: /Property 'escape' is missing/,
			palette: /Property 'escape' is missing/,
			notice: /Property 'escape' is missing/,
			disabledNoticeEscape: /'disabled' does not exist in type 'NoticeEscape'/,
		};
		expect(Object.keys(refusedFor).sort()).toEqual(
			Object.keys(fixtures)
				.filter((name) => name !== "accepted")
				.sort(),
		);
		for (const [name, reason] of Object.entries(refusedFor)) {
			expect(errors(name).join("\n"), `${name} compiled`).toMatch(reason);
		}
	});
});

// The kit runs under two Vitest projects with different cwds (its own config at
// the package root, and the host's `pure` project at the frontend code root).
function kitLayoutDir(): string {
	for (const candidate of ["src/layout", "packages/codefly-ui/src/layout"]) {
		const dir = resolve(process.cwd(), candidate);
		if (existsSync(join(dir, "escape.ts"))) return dir;
	}
	throw new Error("could not locate the @codefly-dev/ui layout directory");
}
