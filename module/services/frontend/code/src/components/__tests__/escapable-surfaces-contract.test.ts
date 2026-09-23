import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";

// Every surface that can cover the page must offer a way out, and the kit is
// where that is guaranteed: `DialogContent`/`SheetContent`/`CommandDialog`
// refuse `showCloseButton={false}` without an `escape` at the type level,
// `Notice` requires one, and an `AlertDialog` carries an `AlertDialogCancel`.
// None of that holds for a surface application code builds by hand — a Terms
// banner written as a raw `<div role="dialog">` covered the account menu and
// left a user with no way to dismiss it or sign out.
//
// So application source may not build one by hand. This walks every `.tsx`
// under `src/` and refuses:
//   - a raw element (lowercase tag) with `role="dialog"` or
//     `role="alertdialog"` — compose `Dialog`, `AlertDialog`, `Sheet` or
//     `Notice` from the kit instead;
//   - `showCloseButton` set to anything but `true` on `DialogContent`,
//     `SheetContent` or `CommandDialog` without an `escape` beside it (the type
//     refuses it too; this names the file even where a cast hid it);
//   - an `AlertDialogContent` with no `AlertDialogCancel` inside it, which no
//     type can see because the cancel is composed as a child.

const BLOCKING_SURFACES = new Set([
	"DialogContent",
	"SheetContent",
	"CommandDialog",
]);
const DIALOG_ROLES = new Set(["dialog", "alertdialog"]);

interface Violation {
	line: number;
	rule:
		| "raw-dialog-role"
		| "close-button-without-escape"
		| "alert-without-cancel";
	message: string;
}

type Opening = ts.JsxOpeningElement | ts.JsxSelfClosingElement;

function attribute(
	element: Opening,
	name: string,
): ts.JsxAttribute | undefined {
	return element.attributes.properties.find(
		(p): p is ts.JsxAttribute =>
			ts.isJsxAttribute(p) && p.name.getText() === name,
	);
}

// A role given as a literal or computed from literals (`role={x ? "dialog" : …}`)
// counts; a role read from a variable is out of this static check's reach.
function namesDialogRole(value: ts.JsxAttributeValue | undefined): boolean {
	if (!value) return false;
	let found = false;
	function visit(node: ts.Node) {
		if (
			(ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) &&
			DIALOG_ROLES.has(node.text)
		)
			found = true;
		ts.forEachChild(node, visit);
	}
	visit(value);
	return found;
}

function showsCloseButton(value: ts.JsxAttributeValue | undefined): boolean {
	// A bare `showCloseButton` is `true`; so is `{true}`. Anything else — a
	// literal false, a computed boolean — takes the button away.
	if (!value) return true;
	return (
		ts.isJsxExpression(value) &&
		value.expression?.kind === ts.SyntaxKind.TrueKeyword
	);
}

function containsTag(node: ts.Node, tag: string): boolean {
	let found = false;
	function visit(child: ts.Node) {
		if (
			(ts.isJsxOpeningElement(child) || ts.isJsxSelfClosingElement(child)) &&
			child.tagName.getText() === tag
		)
			found = true;
		if (!found) ts.forEachChild(child, visit);
	}
	visit(node);
	return found;
}

function escapeViolations(fileName: string, source: string): Violation[] {
	const file = ts.createSourceFile(
		fileName,
		source,
		ts.ScriptTarget.Latest,
		true,
		ts.ScriptKind.TSX,
	);
	const violations: Violation[] = [];
	const line = (node: ts.Node) =>
		file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1;

	function visit(node: ts.Node) {
		if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
			const tag = node.tagName.getText(file);
			if (
				/^[a-z]/.test(tag) &&
				namesDialogRole(attribute(node, "role")?.initializer)
			)
				violations.push({
					line: line(node),
					rule: "raw-dialog-role",
					message: `<${tag} role=…dialog> is a hand-built blocking surface; compose Dialog, AlertDialog, Sheet or Notice from @codefly-dev/ui/layout`,
				});
			const close = attribute(node, "showCloseButton");
			if (
				BLOCKING_SURFACES.has(tag) &&
				close &&
				!showsCloseButton(close.initializer) &&
				!attribute(node, "escape")
			)
				violations.push({
					line: line(node),
					rule: "close-button-without-escape",
					message: `<${tag}> hides its close button without an escape; pass escape={…} naming the way out`,
				});
		}
		if (
			ts.isJsxElement(node) &&
			node.openingElement.tagName.getText(file) === "AlertDialogContent" &&
			!containsTag(node, "AlertDialogCancel")
		)
			violations.push({
				line: line(node),
				rule: "alert-without-cancel",
				message:
					"<AlertDialogContent> has no <AlertDialogCancel>; every alert dialog offers a way to decline",
			});
		ts.forEachChild(node, visit);
	}
	visit(file);
	return violations;
}

function sourceFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const path = join(dir, entry.name);
		if (entry.isDirectory())
			return entry.name === "__tests__" || entry.name === "gen"
				? []
				: sourceFiles(path);
		return entry.name.endsWith(".tsx") && !entry.name.includes(".test.")
			? [path]
			: [];
	});
}

const root = join(process.cwd(), "src");
const files = sourceFiles(root);

describe("application surfaces always have a way out", () => {
	it("scans the application tree, including the known blocking surfaces", () => {
		// A green run must mean "found nothing", never "scanned nothing".
		const scanned = files.map((path) => relative(root, path));
		expect(scanned.length).toBeGreaterThan(100);
		expect(scanned).toContain(join("components", "consent-banner.tsx"));
		expect(scanned).toContain(
			join("features", "api-keys", "ui", "api-key-form.tsx"),
		);
	});

	it("builds no blocking surface by hand and never hides its escape", () => {
		const found = files.flatMap((path) =>
			escapeViolations(path, readFileSync(path, "utf8")).map(
				(v) => `${relative(root, path)}:${v.line} ${v.message}`,
			),
		);
		expect(found).toEqual([]);
	});
});

// The detector is only worth its green run if it fires. Each rule is proven
// against a violating snippet and a compliant one.
describe("escape detector (self-test)", () => {
	const rules = (source: string) =>
		escapeViolations("fixture.tsx", source).map((v) => v.rule);

	it("refuses a raw element with a dialog role, literal or computed", () => {
		expect(rules(`<div role="dialog" />`)).toEqual(["raw-dialog-role"]);
		expect(rules(`<section role={"alertdialog"}>x</section>`)).toEqual([
			"raw-dialog-role",
		]);
		expect(rules(`<div role={open ? "dialog" : undefined} />`)).toEqual([
			"raw-dialog-role",
		]);
		expect(rules(`<div role="status" />`)).toEqual([]);
		// A kit component passing a role through is not a raw element.
		expect(rules(`<Notice role="dialog" />`)).toEqual([]);
	});

	it("refuses a hidden close button without an escape", () => {
		expect(rules(`<DialogContent showCloseButton={false} />`)).toEqual([
			"close-button-without-escape",
		]);
		expect(
			rules(`<SheetContent showCloseButton={busy}>x</SheetContent>`),
		).toEqual(["close-button-without-escape"]);
		expect(
			rules(`<CommandDialog showCloseButton={false}>x</CommandDialog>`),
		).toEqual(["close-button-without-escape"]);
		expect(
			rules(`<DialogContent showCloseButton={false} escape={<Out />} />`),
		).toEqual([]);
		expect(rules(`<DialogContent showCloseButton />`)).toEqual([]);
		expect(rules(`<DialogContent showCloseButton={true} />`)).toEqual([]);
		// DialogFooter's showCloseButton adds a second close; false removes nothing.
		expect(rules(`<DialogFooter showCloseButton={false} />`)).toEqual([]);
	});

	it("refuses an alert dialog without a cancel", () => {
		expect(
			rules(
				`<AlertDialogContent><AlertDialogFooter><AlertDialogAction /></AlertDialogFooter></AlertDialogContent>`,
			),
		).toEqual(["alert-without-cancel"]);
		expect(
			rules(
				`<AlertDialogContent><AlertDialogFooter><AlertDialogCancel>No</AlertDialogCancel></AlertDialogFooter></AlertDialogContent>`,
			),
		).toEqual([]);
	});
});
