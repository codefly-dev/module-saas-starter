import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";
import { expect, it } from "vitest";

function sourceFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const path = join(dir, entry.name);
		return entry.isDirectory()
			? sourceFiles(path)
			: entry.name.endsWith(".tsx") && !entry.name.includes(".test.")
				? [path]
				: [];
	});
}

it("every application Select supplies labels before its lazy popup mounts", () => {
	const missing: string[] = [];
	let checked = 0;
	const root = join(process.cwd(), "src");
	for (const path of sourceFiles(root)) {
		const file = ts.createSourceFile(
			path,
			readFileSync(path, "utf8"),
			ts.ScriptTarget.Latest,
			true,
			ts.ScriptKind.TSX,
		);
		function visit(node: ts.Node) {
			if (
				ts.isJsxElement(node) &&
				node.openingElement.tagName.getText(file) === "Select"
			) {
				checked++;
				const items = node.openingElement.attributes.properties.some(
					(p) =>
						ts.isJsxAttribute(p) &&
						p.name.getText(file) === "items" &&
						!!p.initializer,
				);
				let explicitLabel = false;
				function findLabel(child: ts.Node) {
					if (
						ts.isJsxElement(child) &&
						child.openingElement.tagName.getText(file) === "SelectValue"
					) {
						explicitLabel ||= child.children.some((c) =>
							ts.isJsxExpression(c)
								? !!c.expression
								: ts.isJsxText(c)
									? !!c.text.trim()
									: true,
						);
					}
					ts.forEachChild(child, findLabel);
				}
				findLabel(node);
				if (!items && !explicitLabel)
					missing.push(
						`${relative(root, path)}:${file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1}`,
					);
			}
			ts.forEachChild(node, visit);
		}
		visit(file);
	}
	expect(checked).toBeGreaterThanOrEqual(18);
	expect(
		missing,
		"Provide items labels or explicit SelectValue content; popup-only labels expose raw values",
	).toEqual([]);
});
