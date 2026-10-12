import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";
import { expect, it } from "vitest";

// A MANUAL PASS IS AN ADMISSION. No surface in this repository may reach the
// sync engines by a route that carries no effect identity: `syncSource` did,
// so a person's pass and a declared schedule's occurrence were two admissions
// of two shapes, and it is deleted rather than deprecated (issue #1094).
//
// It is an AST walk and not a grep, for the reason #1090's
// `TestNoHostTimerFiresTheReconcileSchedule` is one. A grep matches the prose
// in this comment and the doc blocks explaining why the method left, and it
// misses `client["syncSource"]` entirely.
//
// WHAT IT ALLOWS, AND WHY THAT IS A FILE AND NOT A NAME. The generated SDK
// still has a `syncSource` — the `SyncSource` RPC, untouched, which is how a
// RECONNECT rebinds whose authority a source's syncs run under. An earlier
// version of this gate allowed it by the RECEIVER's name (`client.syncSource`
// is the SDK, anything else is the kit) and that gate was worthless: the
// deleted caller was spelled `client.syncSource(orgId, source.id)` on the
// kit's own client prop, so the one path this test exists to forbid would have
// walked straight through it. The discriminator is the file. The kit has
// exactly one transport adapter, `gateway.ts`, and that is the only place the
// generated client is reached at all — so the RPC is allowed there and nowhere
// else, and inside it every `syncSource` call must carry `reconnect: true`,
// which is the property that makes it a reconnect rather than a sync.

/** The directories whose authored TypeScript this walk judges. */
const ROOTS = [
	"src",
	"packages/saas-ui/src",
	"packages/saas-ui/stories",
	"packages/codefly-ui/src",
];

/**
 * The one file that may reach the generated `SyncSource` RPC: the kit's single
 * transport adapter. Its calls are checked for `reconnect: true` rather than
 * exempted.
 */
const TRANSPORT_ADAPTER = join("packages", "saas-ui", "src", "datasources", "gateway.ts");

function sources(directory: string): string[] {
	return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
		const path = join(directory, entry.name);
		if (entry.isDirectory())
			return entry.name === "gen" || entry.name === "node_modules"
				? []
				: sources(path);
		// A test's own fake client is judged here too, not excluded: a fake that
		// re-offers the removed method is a test asserting a contract the kit no
		// longer has.
		return /\.tsx?$/.test(entry.name) ? [path] : [];
	});
}

it("no surface reaches a source sync without an effect identity", () => {
	const offenders: string[] = [];
	let walked = 0;
	let reconnects = 0;
	for (const root of ROOTS) {
		for (const path of sources(root)) {
			const adapter = path.endsWith(TRANSPORT_ADAPTER);
			const file = ts.createSourceFile(
				path,
				readFileSync(path, "utf8"),
				ts.ScriptTarget.Latest,
				true,
				path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
			);
			walked++;
			const report = (node: ts.Node, what: string) =>
				offenders.push(
					`${relative(process.cwd(), path)}:${
						file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1
					} — ${what}`,
				);
			const visit = (node: ts.Node) => {
				const named = reachesSyncSource(node);
				if (named) {
					if (!adapter) report(node, `${named} outside the transport adapter`);
					else if (!isReconnectCall(node.parent)) {
						report(node, "a SyncSource call that is not a reconnect");
					} else reconnects++;
				}
				// `syncSource:` offered by an object, a class or an interface — a
				// client implementation, or a fake re-offering the removed method.
				if (
					(ts.isPropertyAssignment(node) ||
						ts.isMethodDeclaration(node) ||
						ts.isMethodSignature(node) ||
						ts.isPropertySignature(node)) &&
					propertyName(node.name) === "syncSource"
				)
					report(node, "a declaration offering syncSource");
				ts.forEachChild(node, visit);
			};
			ts.forEachChild(file, visit);
		}
	}
	// Two guards against a walk that passed because it saw nothing: it read the
	// tree, and it found the one legitimate use it is written to allow. Without
	// the second, deleting the reconnect would make this gate silently vacuous.
	expect(walked).toBeGreaterThan(100);
	expect(reconnects, "the reconnect still reaches SyncSource").toBe(1);
	expect(offenders, "a manual sync must admit ReconcileSource").toEqual([]);
});

/** `x.syncSource` or `x["syncSource"]`, however the receiver is spelled. */
function reachesSyncSource(node: ts.Node): string | undefined {
	if (ts.isPropertyAccessExpression(node) && node.name.text === "syncSource")
		return "syncSource";
	if (
		ts.isElementAccessExpression(node) &&
		ts.isStringLiteralLike(node.argumentExpression) &&
		node.argumentExpression.text === "syncSource"
	)
		return 'syncSource reached as ["syncSource"]';
	return undefined;
}

/** True when this call hands the RPC `reconnect: true`, making it a reconnect. */
function isReconnectCall(node: ts.Node | undefined): boolean {
	if (!node || !ts.isCallExpression(node)) return false;
	return node.arguments.some(
		(argument) =>
			ts.isObjectLiteralExpression(argument) &&
			argument.properties.some(
				(property) =>
					ts.isPropertyAssignment(property) &&
					propertyName(property.name) === "reconnect" &&
					property.initializer.kind === ts.SyntaxKind.TrueKeyword,
			),
	);
}

function propertyName(name: ts.PropertyName): string {
	return ts.isIdentifier(name) || ts.isStringLiteralLike(name) ? name.text : "";
}
