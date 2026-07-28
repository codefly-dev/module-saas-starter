import fs from "node:fs";
import path from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";

describe("production chart data policy", () => {
	it("does not render literal numeric series as account data", () => {
		const sourceRoot = path.resolve(process.cwd(), "src");
		const productionSources = walk(sourceRoot).filter(
			(file) =>
				/\.(ts|tsx)$/.test(file) &&
				!file.includes("__tests__") &&
				!file.endsWith(".test.ts") &&
				!file.endsWith(".test.tsx"),
		);
		const violations = productionSources.filter((file) =>
			containsLiteralChartSeries(fs.readFileSync(file, "utf8"), file),
		);
		expect(violations).toEqual([]);
	});

	it("detects a literal series passed through a variable", () => {
		expect(
			containsLiteralChartSeries(
				"const fakeActivity = [3, 5, 4]; <Sparkline points={fakeActivity} />",
				"fixture.tsx",
			),
		).toBe(true);
	});
});

function containsLiteralChartSeries(source: string, fileName: string): boolean {
	const file = ts.createSourceFile(
		fileName,
		source,
		ts.ScriptTarget.Latest,
		true,
		ts.ScriptKind.TSX,
	);
	const initializers = new Map<string, ts.Expression>();
	const collect = (node: ts.Node) => {
		if (
			ts.isVariableDeclaration(node) &&
			ts.isIdentifier(node.name) &&
			node.initializer
		) {
			initializers.set(node.name.text, node.initializer);
		}
		ts.forEachChild(node, collect);
	};
	collect(file);

	const resolvesToNumericSeries = (
		expression: ts.Expression,
		visited = new Set<string>(),
	): boolean => {
		if (ts.isArrayLiteralExpression(expression)) {
			return (
				expression.elements.length >= 2 &&
				expression.elements.every(isNumericLiteral)
			);
		}
		if (ts.isIdentifier(expression)) {
			if (visited.has(expression.text)) return false;
			const initializer = initializers.get(expression.text);
			if (!initializer) return false;
			visited.add(expression.text);
			return resolvesToNumericSeries(initializer, visited);
		}
		if (
			ts.isAsExpression(expression) ||
			ts.isParenthesizedExpression(expression) ||
			ts.isSatisfiesExpression(expression)
		) {
			return resolvesToNumericSeries(expression.expression, visited);
		}
		return false;
	};

	let violation = false;
	const inspect = (node: ts.Node) => {
		if (
			ts.isJsxAttribute(node) &&
			["points", "series", "data"].includes(node.name.getText(file)) &&
			node.initializer &&
			ts.isJsxExpression(node.initializer) &&
			node.initializer.expression &&
			resolvesToNumericSeries(node.initializer.expression)
		) {
			violation = true;
			return;
		}
		ts.forEachChild(node, inspect);
	};
	inspect(file);
	return violation;
}

function isNumericLiteral(node: ts.Expression): boolean {
	return (
		ts.isNumericLiteral(node) ||
		(ts.isPrefixUnaryExpression(node) &&
			node.operator === ts.SyntaxKind.MinusToken &&
			ts.isNumericLiteral(node.operand))
	);
}

function walk(directory: string): string[] {
	return fs.readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
		const target = path.join(directory, entry.name);
		return entry.isDirectory() ? walk(target) : [target];
	});
}
