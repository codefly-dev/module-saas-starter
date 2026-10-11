import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";
import packageJson from "../../../package.json" with { type: "json" };

/**
 * The host app composes `@codefly-dev/ui`; it does not re-implement it.
 *
 * The audit that opened issue #1041 found a solution's Dashboard tab drawn with
 * the kit's older basic chart tier while the host's own operations pages used
 * its metric tier — because `src/solutions/SolutionDashboard.tsx` had its own
 * copy of the widget-to-chart dispatch, the responsive grid, the card shell and
 * the empty state beside the kit's `<Dashboard>`. Two renderers of one thing do
 * not stay equal: one gets the sparkline, the crosshair readout and the
 * screen-reader data table a release later, and the other does not. The owner
 * asked for a linter.
 *
 * The rule is not invented here: another module that composes this kit already
 * carries it, in a gate of the same shape, and that one is copied rather than
 * replaced. Three legs, because no single one of them is sufficient:
 *
 *   - **A name the kit exports** may be declared here only by a wrapper that
 *     renders the kit's component of that name, or with a reviewed entry in
 *     {@link ALLOWED}. Both forms of the declaration count — exported and
 *     module-local — because a duplicate hidden as a file-local helper is the
 *     commonest way one arrives (two of them were sitting here when this gate
 *     was written, and the upstream version of this rule, which looked only at
 *     exported declarations, would have walked past both).
 *   - **`class-variance-authority`** — the library the kit builds its variants
 *     with — is not a dependency of this package, so a kit-shaped primitive
 *     cannot be built here at all. This leg is a fact, not a convention, which
 *     is why it is worth more than the first.
 *   - **The chart surfaces stay re-exports.** `src/components/charts/chart.tsx`
 *     and `src/features/dashboard/ui/charts/line-chart.tsx` are each a single
 *     `export … from "@codefly-dev/ui/dashboard"`. Issue #1041 asked what they
 *     were; they are already right, and this keeps them so.
 *
 * It resolves names with the TypeScript checker, so every spelling that reaches
 * the same component reaches the same verdict, and it reads what a component
 * RETURNS rather than what a file mentions. Where it cannot resolve a name the
 * kit owns — published from a third-party module, or from a chain whose end it
 * cannot read — it refuses rather than passing: "could not tell" is not a
 * verdict it is allowed to reach, and four rounds of adversarial fixtures each
 * found the next way through the version that skipped instead.
 *
 * WHAT IT CANNOT SEE. This is a tripwire for the common shape, not a proof, and
 * these are limits rather than bugs — each is a place where deciding would mean
 * following the program further than reading one component's returned
 * expression:
 *
 *   - **A duplicate under a NEW name**, built by hand, passes every leg.
 *     {@link ALLOWED} and {@link DISPATCHERS} are the backstop — labels a
 *     reviewer reads and asks about, not detectors.
 *   - **Kit JSX handed to another component through an attribute.** What the
 *     recipient does with a prop is beyond this; a kit element in one is no
 *     evidence that this component renders it. Children are not attributes and
 *     do count.
 *   - **Anything behind a call.** A mark rendered by a helper function, or
 *     chosen from a lookup table (`MARKS[kind]`), is a dispatch this cannot
 *     read. The table is plain to a reviewer and invisible here.
 *   - **A dispatch split across components**, one visualization each, composed
 *     by a parent that only forwards.
 *
 * Classifying files by their names instead would fail on the next file anyone
 * adds, and a gate that cries wolf is a gate that gets deleted.
 */

const here = dirname(fileURLToPath(import.meta.url));
const appRoot = join(here, "..", "..", "..");

/**
 * A name this package declares although the kit exports it, and why. Each entry
 * says what the two things are, why neither can stand in for the other, and
 * what would have to change for the entry to go.
 */
const ALLOWED: Record<string, string> = {
	"Dashboard in src/features/dashboard/ui/dashboard.tsx":
		"the host's dashboard DSL surface: it reads the viewer's organization from host auth and resolves each declared metric through `useMetric` as it draws. The kit's `<Dashboard>` is pure and takes an already-resolved `DashboardView`, so neither can stand in for the other. Goes when the DSL resolves its metrics outside the renderer and hands the kit a view, as `src/solutions/SolutionDashboard.tsx` now does.",
	"Dashboard in src/components/dashboard.tsx":
		"the widget-slot renderer: a discriminated union of host-supplied widgets (sparkline, bars, series, arbitrary node), each carrying its own loading/error/empty state, rather than a declared metric view. Goes when the audit page's widgets are expressible as a `DashboardView` and it mounts the kit's renderer instead.",
	"Layout in src/features/dashboard/ui/layout.tsx":
		"page rhythm for a dashboard page — an `<h1>` and a vertical stack. The kit's `Layout` is the application shell (sidebar, header, content region); this is what goes inside one. Goes when it is re-expressed on the kit's `Page`/`PageHeader`.",
	"MetricCard in src/features/dashboard/ui/metric-card.tsx":
		"resolves one declared metric through `useMetric` and then paints whichever chart the declaration asked for; the kit's `MetricCard` takes an already-resolved `Metric`. Goes with the DSL's resolution moving outside the card, the same change that retires the `Dashboard` entry above.",
	"MetricCard in src/features/event-operations/ui/event-operations-page.tsx":
		"a tile over an already-formatted string value plus a caption line. The kit's `StatTile`/`MetricCard` take a numeric `Metric` and have no caption slot, so this is not yet expressible as one. Goes when `StatTile` carries a description, or when these counts are expressed as `Metric`s with a `format`.",
	"MetricCard in src/features/job-operations/ui/job-operations-page.tsx":
		"the same tile as the event-operations page's, over the job queue's counts, and it goes with it — the two are each other's duplicate as much as the kit's, and both should become one call to the kit's tile.",
};

/**
 * Chart surfaces that are, and stay, thin re-exports of the kit.
 *
 * Issue #1041 asked what these two are, since both carry a kit export's name.
 * They are each a single `export … from "@codefly-dev/ui/dashboard"`: they ship
 * the kit's component, not another one, so neither is a duplicate and neither
 * needs an entry in {@link ALLOWED}. What they do carry is a hazard this gate
 * cannot see, recorded here rather than left for the next reader to find: both
 * publish the name `LineChart`, and they publish DIFFERENT kit components under
 * it — `src/components/charts` re-exports the metric-tier `LineChart`, while
 * `src/features/dashboard/ui/charts` re-exports `TrendLineChart`. An import of
 * `LineChart` therefore means one of two charts depending on which path it came
 * from. Retiring the second alias belongs with the dashboard-DSL work that
 * retires `MetricCard in src/features/dashboard/ui/metric-card.tsx` below, its
 * only consumer.
 */
const CHART_RE_EXPORTS = [
	"src/components/charts/chart.tsx",
	"src/features/dashboard/ui/charts/line-chart.tsx",
];

function walk(dir: string, out: string[] = []): string[] {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) {
			if (entry.name !== "__tests__" && entry.name !== "node_modules") {
				walk(path, out);
			}
			continue;
		}
		if (/\.tsx?$/.test(entry.name) && !/\.(test|stories)\./.test(entry.name)) {
			out.push(path);
		}
	}
	return out;
}

/**
 * `export *` clauses in the kit's entry points, by the module they name.
 *
 * The export scan below reads `export { … }` blocks, so a star re-export is
 * invisible to it: the kit's root and plugin-host entry points use one, and
 * four capitalised names reach consumers only that way. None collides with a
 * host declaration today, but a partial miss is indistinguishable from success
 * — the `kit.size` sanity check passes at 309 whether those four are in it or
 * not — so the gate refuses a star it cannot resolve rather than quietly
 * covering less than it claims.
 */
function starReExports(
	entryPoints: { path: string; source: string }[],
): string[] {
	const unresolved: string[] = [];
	for (const { path, source } of entryPoints) {
		for (const star of source.matchAll(
			/export\s+\*\s+from\s*["']([^"']+)["']/g,
		)) {
			// A star onto another of the kit's own entry points resolves: that
			// file is itself in this list and its named exports are read there.
			if (star[1].startsWith("./") || star[1].startsWith("../")) continue;
			unresolved.push(`${path} → ${star[1]}`);
		}
	}
	return unresolved;
}

/** Every capitalised name the kit's entry points export. */
function kitExports(entryPoints: string[]): Set<string> {
	const names = new Set<string>();
	for (const source of entryPoints) {
		for (const block of source.matchAll(/export\s*\{([^}]*)\}/g)) {
			for (const raw of block[1].split(",")) {
				const name =
					raw
						.trim()
						.replace(/^type\s+/, "")
						.split(/\s+as\s+/)
						.pop()
						?.trim() ?? "";
				if (/^[A-Z][A-Za-z0-9]*$/.test(name)) names.add(name);
			}
		}
	}
	return names;
}

/**
 * Modules of this app that exist only to re-export the kit — the host's own
 * import surface. `src/shared/ui/index.ts` says so in its first line ("Host
 * convenience imports; presentation is implemented by the shared kit"), and
 * most of the app reaches the kit through it rather than through
 * `@codefly-dev/ui` directly.
 *
 * They are kit sources for the purposes of the rule below. Without this, the
 * gate's verdict depended on which path a wrapper imported through: the same
 * `Badge` wrapper passed when it imported `@codefly-dev/ui/layout` and failed
 * when it imported the identical component from `@/shared/ui`. A gate that
 * rejects the house style is one that gets an exception entry per correct file,
 * and then gets deleted.
 */
const KIT_SURFACES = [
	"src/shared/ui/index.ts",
	"src/components/charts/chart.tsx",
	"src/components/charts/index.ts",
	"src/components/sparkline.tsx",
	"src/components/metric-state.tsx",
];

const KIT_SPECIFIER = /^@codefly-dev\/(?:ui|saas-ui)(?:\/|$)/;

/**
 * Whether a module specifier names one of the host's kit surfaces, by alias
 * (`@/shared/ui`) or by a relative path that lands on the same file. Both
 * spellings reach the same module, so judging only the alias rejected a real
 * wrapper for how it spelled its import.
 */
function isHostSurface(specifier: string, fromPath: string): boolean {
	const resolved = specifier.startsWith("@/")
		? `src/${specifier.slice(2)}`
		: specifier.startsWith(".")
			? join(dirname(fromPath), specifier).replaceAll("\\", "/")
			: null;
	if (resolved === null) return false;
	return KIT_SURFACES.some(
		(surface) => surface === resolved || surface.startsWith(`${resolved}/`),
	);
}

/**
 * What a local name in a file refers to.
 *
 * `{ kit }` is a kit component, under the kit's OWN name whatever the local
 * spelling. `{ namespace }` is a whole module imported as one object, so
 * `<D.LineChart/>` has to be resolved through it at the use site.
 */
/**
 * A program over just these sources, so a symbol can be resolved the way the
 * compiler resolves it.
 *
 * Hand-rolled scope analysis is what kept misjudging this: it collected names
 * across scopes (so a nested parameter shadowed an outer tag), missed
 * destructured bindings (so a parameter did NOT shadow when it should have),
 * and could not follow an export chain at all. The checker answers all of
 * those from one question — what does this identifier refer to — so the rules
 * below ask it instead of re-deriving it.
 *
 * Only these files are in the program: relative specifiers resolve between
 * them and everything else (`react`, the kit itself) stays unresolved on
 * purpose. A kit binding is recognised from the import declaration in the
 * source, which is present whether or not the package is installed, so the
 * gate needs no `node_modules` and cannot be weakened by a resolution failure.
 */
function programOver(files: { path: string; source: string }[]): {
	program: ts.Program;
	checker: ts.TypeChecker;
	sources: Map<string, ts.SourceFile>;
} {
	const sources = new Map(
		files.map(({ path, source }) => [path, parse(path, source)]),
	);
	const host: ts.CompilerHost = {
		fileExists: (name) => sources.has(name),
		readFile: (name) => sources.get(name)?.text,
		getSourceFile: (name) => sources.get(name),
		getDefaultLibFileName: () => "lib.d.ts",
		writeFile: () => {},
		getCurrentDirectory: () => "",
		getCanonicalFileName: (name) => name,
		useCaseSensitiveFileNames: () => true,
		getNewLine: () => "\n",
		resolveModuleNames: (names, containingFile) =>
			names.map((name) => {
				// The app's own two spellings of its own modules: a relative path
				// and the `@/` alias its tsconfig maps to `src/`. Resolving only
				// the first let a copy hide behind an export chain written in the
				// second — the gate read the barrel and then stopped.
				if (!name.startsWith(".") && !name.startsWith("@/")) return undefined;
				const base = name.startsWith("@/")
					? `src/${name.slice(2)}`
					: join(dirname(containingFile), name).replaceAll("\\", "/");
				for (const candidate of [
					base,
					`${base}.tsx`,
					`${base}.ts`,
					`${base}/index.tsx`,
					`${base}/index.ts`,
				]) {
					if (sources.has(candidate)) {
						return {
							resolvedFileName: candidate,
							extension: candidate.endsWith(".tsx")
								? ts.Extension.Tsx
								: ts.Extension.Ts,
						};
					}
				}
				return undefined;
			}),
	};
	const program = ts.createProgram(
		[...sources.keys()],
		{
			jsx: ts.JsxEmit.Preserve,
			noLib: true,
			noResolve: false,
			allowJs: false,
			target: ts.ScriptTarget.Latest,
			module: ts.ModuleKind.ESNext,
		},
		host,
	);
	return { program, checker: program.getTypeChecker(), sources };
}

const KIT_NAMESPACE = "*kit";
const SURFACE_NAMESPACE = "*surface";

/**
 * An alias chain from a symbol to its end, every hop included.
 *
 * Cycle-detected, not hop-capped. A chain that is long is not a chain that is
 * suspicious, and abandoning one at an arbitrary depth was itself an escape:
 * nine re-exports in a row resolved to nothing and the copy at the end of them
 * went unexamined. A cycle is the only thing that can stop this, and it stops
 * it without a verdict.
 */
function aliasChain(symbol: ts.Symbol, checker: ts.TypeChecker): ts.Symbol[] {
	const chain = [symbol];
	const seen = new Set<ts.Symbol>([symbol]);
	let current = symbol;
	while (current.flags & ts.SymbolFlags.Alias) {
		const next = checker.getImmediateAliasedSymbol(current);
		if (!next || seen.has(next)) break;
		seen.add(next);
		chain.push(next);
		current = next;
	}
	return chain;
}

/**
 * Where a declaration comes from, when it comes from another module.
 *
 * `kit` is the kit's own component under the kit's own name, however the local
 * spelling renamed it, whether it arrived by `import` or by `export … from`.
 * `namespace` is a whole kit or surface module, to be resolved at the use site.
 * `external` is a module that is neither — including a third-party one, which
 * is as much a second implementation of a kit name as a hand-written copy.
 * `undefined` means the declaration is local, so the chain continues.
 */
type KitBinding =
	| { kind: "kit"; name: string }
	| { kind: "namespace"; marker: string }
	| { kind: "type" }
	| { kind: "external"; from: string };

function kitBindingOf(
	declaration: ts.Node,
	surface: Map<string, string>,
): KitBinding | undefined {
	let from: ts.ImportDeclaration | ts.ExportDeclaration | undefined;
	for (let node: ts.Node | undefined = declaration; node; node = node.parent) {
		if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) {
			from = node;
			break;
		}
	}
	const specifier = from?.moduleSpecifier;
	if (!from || !specifier || !ts.isStringLiteral(specifier)) return undefined;
	if (
		(ts.isImportDeclaration(from) && from.importClause?.isTypeOnly) ||
		(ts.isExportDeclaration(from) && from.isTypeOnly)
	) {
		return { kind: "type" };
	}
	const fromKit = KIT_SPECIFIER.test(specifier.text);
	const fromSurface = isHostSurface(
		specifier.text,
		from.getSourceFile().fileName,
	);
	if (!fromKit && !fromSurface) {
		return { kind: "external", from: specifier.text };
	}
	if (ts.isNamespaceImport(declaration) || ts.isNamespaceExport(declaration)) {
		return {
			kind: "namespace",
			marker: fromKit ? KIT_NAMESPACE : SURFACE_NAMESPACE,
		};
	}
	if (ts.isImportSpecifier(declaration) || ts.isExportSpecifier(declaration)) {
		// A type is not a component. It cannot be rendered, it cannot be a
		// second implementation of one, and treating a type-only binding as
		// something unreadable would offend on every `export type` that shares
		// a kit name.
		if (declaration.isTypeOnly || typeOnlyClause(declaration)) {
			return { kind: "type" };
		}
		const published = (declaration.propertyName ?? declaration.name).text;
		const name = fromKit ? published : surface.get(published);
		return name
			? { kind: "kit", name }
			: { kind: "external", from: specifier.text };
	}
	return { kind: "external", from: specifier.text };
}

/** Whether the clause a specifier sits in is type-only. */
function typeOnlyClause(
	declaration: ts.ImportSpecifier | ts.ExportSpecifier,
): boolean {
	const clause = declaration.parent.parent;
	return ts.isImportClause(clause)
		? clause.isTypeOnly
		: ts.isExportDeclaration(clause) && clause.isTypeOnly;
}

/**
 * The kit component an identifier refers to, by the kit's OWN name, or a
 * namespace marker for a whole kit module, or undefined for anything else —
 * including anything that cannot be resolved, which is the conservative
 * answer: unresolved is not composition.
 *
 * The checker answers this, so every spelling that reaches the same component
 * reaches the same verdict: an import alias, a local variable alias, a chain of
 * re-exports through the app's own barrels, a member of a namespace import of
 * either. Hand-rolled scope analysis is what kept misjudging it — it collected
 * names across scopes, missed destructured bindings, and could not follow an
 * export chain at all.
 */
function kitComponentOf(
	node: ts.Expression | ts.JsxTagNameExpression,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
	seen = new Set<ts.Node>(),
): string | undefined {
	if (seen.has(node)) return undefined;
	seen.add(node);

	// The whole expression first, which is what resolves `UI.Card` when `UI` is
	// a namespace of one of the app's own barrels: the checker walks the barrel's
	// export to the kit import behind it.
	const symbol = checker.getSymbolAtLocation(node);
	if (symbol) {
		for (const hop of aliasChain(symbol, checker)) {
			const declaration = hop.declarations?.[0];
			if (!declaration) continue;
			const binding = kitBindingOf(declaration, surface);
			if (binding?.kind === "kit") return binding.name;
			if (binding?.kind === "namespace") return binding.marker;
			// Another module of the app's own: the chain walks through it. Only
			// its end can say the name is not the kit's, which is why nothing
			// here stops at the first hop that is not.
			if (binding) continue;
			if (ts.isVariableDeclaration(declaration) && declaration.initializer) {
				return kitComponentOf(declaration.initializer, checker, surface, seen);
			}
		}
	}
	// `UI.Card` where `UI` is the kit itself: the kit is not in the program, so
	// there is no symbol for the member and the namespace is the evidence.
	if (ts.isPropertyAccessExpression(node)) {
		const base = kitComponentOf(node.expression, checker, surface, seen);
		if (base === KIT_NAMESPACE) return node.name.text;
		if (base === SURFACE_NAMESPACE) return surface.get(node.name.text);
	}
	return undefined;
}

/**
 * What a component renders, as the alternative pictures it can draw: one set of
 * kit marks per branch the renderer can take.
 *
 * Expression forms, and nothing beyond them. A conditional is two
 * alternatives wherever it sits — nested inside returned JSX included, which
 * is where a dispatch is usually written. `&&` adds the empty alternative,
 * because the mark may be absent. `||` and `??` are their two sides. A cast,
 * a parenthesis and a non-null assertion are their inner expression. A comma
 * expression is its RIGHT operand alone: the others are evaluated and thrown
 * away. Children of an element render together, so they multiply out.
 *
 * Nothing else is descended into, which is how two things end up not counting
 * as rendered — both limits rather than oversights. A function body is not what
 * the expression holding it draws, so a render prop and a callback in children
 * are values, not pictures. And JSX handed to another component through an
 * attribute is a value passed too: what the recipient does with it is beyond
 * anything this can see, so a kit element in a prop is no evidence that this
 * component renders it. Children are not attributes and do count.
 */
function alternatives(
	node: ts.Node,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
	seen = new Set<ts.Node>(),
): Set<string>[] {
	const NOTHING = [new Set<string>()];
	if (seen.has(node)) return NOTHING;
	seen.add(node);
	const of = (next: ts.Node) => alternatives(next, checker, surface, seen);

	if (
		ts.isParenthesizedExpression(node) ||
		ts.isAsExpression(node) ||
		ts.isSatisfiesExpression(node) ||
		ts.isTypeAssertionExpression(node) ||
		ts.isNonNullExpression(node)
	) {
		return of(node.expression);
	}
	if (ts.isJsxExpression(node)) {
		return node.expression ? of(node.expression) : NOTHING;
	}
	if (ts.isConditionalExpression(node)) {
		return distinct([...of(node.whenTrue), ...of(node.whenFalse)]);
	}
	if (ts.isBinaryExpression(node)) {
		switch (node.operatorToken.kind) {
			case ts.SyntaxKind.CommaToken:
				return of(node.right);
			case ts.SyntaxKind.AmpersandAmpersandToken:
				return distinct([new Set<string>(), ...of(node.right)]);
			case ts.SyntaxKind.BarBarToken:
			case ts.SyntaxKind.QuestionQuestionToken:
				return distinct([...of(node.left), ...of(node.right)]);
			default:
				return NOTHING;
		}
	}
	if (ts.isJsxElement(node)) {
		return together([
			[markOf(node.openingElement.tagName, checker, surface)],
			...node.children.map(of),
		]);
	}
	if (ts.isJsxSelfClosingElement(node)) {
		return [markOf(node.tagName, checker, surface)];
	}
	if (ts.isJsxFragment(node)) {
		return together(node.children.map(of));
	}
	if (ts.isArrayLiteralExpression(node)) {
		return together(node.elements.map(of));
	}
	// A local name holding a picture: `const content = <KitCard/>; return content`.
	if (ts.isIdentifier(node)) {
		const symbol = checker.getSymbolAtLocation(node);
		for (const hop of symbol ? aliasChain(symbol, checker) : []) {
			const declaration = hop.declarations?.[0];
			if (
				declaration &&
				ts.isVariableDeclaration(declaration) &&
				declaration.initializer
			) {
				return of(declaration.initializer);
			}
		}
	}
	return NOTHING;
}

/** Whether a node is a function, whose body is not this component's picture. */
function isFunctionLike(node: ts.Node): boolean {
	return (
		ts.isFunctionDeclaration(node) ||
		ts.isFunctionExpression(node) ||
		ts.isArrowFunction(node) ||
		ts.isMethodDeclaration(node)
	);
}

/** The kit mark a JSX tag draws, as a set of none or one. */
function markOf(
	tag: ts.JsxTagNameExpression,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
): Set<string> {
	const kit = kitComponentOf(tag, checker, surface);
	return kit && !kit.startsWith("*") ? new Set([kit]) : new Set<string>();
}

/** One alternative per distinct set of marks. */
function distinct(paths: Set<string>[]): Set<string>[] {
	const byShape = new Map<string, Set<string>>();
	for (const path of paths) {
		byShape.set([...path].sort().join("+"), path);
	}
	return [...byShape.values()];
}

/**
 * Parts that all render: every combination of one alternative from each. Kept
 * distinct at each step, so the count is bounded by the mark sets that can
 * actually occur rather than by the size of the tree.
 */
function together(parts: Set<string>[][]): Set<string>[] {
	let combined: Set<string>[] = [new Set<string>()];
	for (const part of parts) {
		const next: Set<string>[] = [];
		for (const left of combined) {
			for (const right of part) {
				next.push(new Set([...left, ...right]));
			}
		}
		combined = distinct(next);
	}
	return combined;
}

/**
 * The alternatives a declaration's body returns.
 *
 * Every `return` in the body, plus a concise arrow body, which is one. A
 * statement after a `return` or a `throw` in the same block is unreachable and
 * is not one: a picture no execution reaches is not drawn.
 */
function returnedAlternatives(
	declaration: ts.Node,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
): Set<string>[] {
	const body = functionBodyOf(declaration);
	if (!body) return [];
	if (!ts.isBlock(body)) {
		return alternatives(body, checker, surface);
	}
	const paths: Set<string>[] = [];
	const visit = (node: ts.Node) => {
		if (isFunctionLike(node) && node !== body) return;
		if (ts.isBlock(node) || ts.isCaseClause(node) || ts.isDefaultClause(node)) {
			for (const statement of node.statements) {
				visit(statement);
				if (ts.isReturnStatement(statement) || ts.isThrowStatement(statement)) {
					return;
				}
			}
			return;
		}
		if (ts.isReturnStatement(node)) {
			if (node.expression) {
				paths.push(...alternatives(node.expression, checker, surface));
			}
			return;
		}
		ts.forEachChild(node, visit);
	};
	visit(body);
	return paths;
}

/** Every kit component this declaration returns, on any alternative. */
function returnedKitComponents(
	declaration: ts.Node,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
): Set<string> {
	return new Set(
		returnedAlternatives(declaration, checker, surface).flatMap((path) => [
			...path,
		]),
	);
}

/** The body of the function a declaration is, if it is one. */
function functionBodyOf(declaration: ts.Node): ts.Node | undefined {
	if (ts.isFunctionDeclaration(declaration)) return declaration.body;
	if (
		ts.isVariableDeclaration(declaration) &&
		declaration.initializer &&
		isFunctionLike(declaration.initializer)
	) {
		return (declaration.initializer as ts.ArrowFunction).body;
	}
	if (isFunctionLike(declaration)) {
		return (declaration as ts.ArrowFunction).body;
	}
	return undefined;
}

/**
 * What a published name resolves to.
 *
 * `declaration` is code in this set, to be examined. `kit` is the kit's own
 * component, however renamed — a re-export ships the kit's component rather
 * than a second one. `unresolved` is everything else, and it is an OFFENCE
 * rather than a skip: a name the kit owns, published here from somewhere this
 * cannot read, is a second implementation of it as surely as a hand-written
 * copy, and "could not tell" was the hole that four rounds of fixtures each
 * found the next way through.
 */
type Resolution =
	| { kind: "declaration"; node: ts.Node }
	| { kind: "kit"; name: string }
	| { kind: "type" }
	| { kind: "unresolved"; why: string };

/**
 * Every component a file publishes, by published name — following an export
 * chain of any length into whichever file actually declares it, so splitting a
 * copy from its kit-named export hides nothing and a genuine wrapper
 * re-exported under the kit's name is not mistaken for one. Declarations the
 * file does not export are still declarations here: a duplicate hidden as a
 * file-local helper is the commonest way one arrives.
 */
function publishedComponents(
	file: ts.SourceFile,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
): Map<string, Resolution> {
	const published = new Map<string, Resolution>();
	const resolve = (symbol: ts.Symbol): Resolution => {
		const chain = aliasChain(symbol, checker);
		for (const [hop, resolved] of chain.entries()) {
			const declarations = resolved.declarations ?? [];
			const value = declarations.find(
				(declaration) =>
					ts.isFunctionDeclaration(declaration) ||
					ts.isClassDeclaration(declaration) ||
					ts.isVariableDeclaration(declaration),
			);
			if (value) return { kind: "declaration", node: value };
			if (
				declarations.length > 0 &&
				declarations.every(
					(declaration) =>
						ts.isTypeAliasDeclaration(declaration) ||
						ts.isInterfaceDeclaration(declaration),
				)
			) {
				return { kind: "type" };
			}
			const binding = declarations[0]
				? kitBindingOf(declarations[0], surface)
				: undefined;
			if (binding?.kind === "kit" || binding?.kind === "namespace") {
				return {
					kind: "kit",
					name: binding.kind === "kit" ? binding.name : binding.marker,
				};
			}
			if (binding?.kind === "type") return { kind: "type" };
			// An `external` hop in the middle is another of the app's own
			// modules, which the chain walks through. At the END of the chain it
			// is a module this cannot read, and a kit name coming out of one is
			// a second implementation of it as much as a hand-written copy.
			if (hop === chain.length - 1) {
				return binding?.kind === "external"
					? { kind: "unresolved", why: `published from ${binding.from}` }
					: { kind: "unresolved", why: "the chain reaches no declaration" };
			}
		}
		return { kind: "unresolved", why: "the chain reaches no declaration" };
	};

	const moduleSymbol = checker.getSymbolAtLocation(file);
	if (moduleSymbol) {
		for (const exported of checker.getExportsOfModule(moduleSymbol)) {
			if (!/^[A-Z][A-Za-z0-9]*$/.test(exported.name)) continue;
			published.set(exported.name, resolve(exported));
		}
	}
	const visit = (node: ts.Node) => {
		const name =
			(ts.isFunctionDeclaration(node) || ts.isClassDeclaration(node)) &&
			node.name
				? node.name.text
				: ts.isVariableDeclaration(node) && ts.isIdentifier(node.name)
					? node.name.text
					: undefined;
		if (name && /^[A-Z][A-Za-z0-9]*$/.test(name) && !published.has(name)) {
			published.set(name, { kind: "declaration", node });
		}
		ts.forEachChild(node, visit);
	};
	visit(file);
	return published;
}

/**
 * The kit's dashboard marks. A surface may compose several of these on one
 * page — a KPI row above a chart is a page, not a renderer. What it may not do
 * is BRANCH between them: choosing a mark by a widget's kind is a dispatch over
 * visualizations, and that is `<Dashboard>`'s whole job.
 *
 * Branching is the test, not the count, because counting file-wide imports
 * caught a page that composed two marks side by side and could be escaped by a
 * namespace import or a local alias. This is also the only leg that catches a
 * duplicate under a NEW name: the renderer #1041 deleted declared
 * `WidgetBody`, `WidgetValues` and `WidgetCard` — not one of them a name the
 * kit exports — so every name-based leg passed it while it was the duplication
 * itself.
 */
const DASHBOARD_MARKS = new Set([
	"LineChart",
	"AreaChart",
	"BarList",
	"StatChart",
	"TrendLineChart",
	"MetricLineChart",
	"MetricAreaChart",
	"MetricBarChart",
	"StatTile",
	"MetricCard",
	"KPIRow",
	"Sparkline",
]);

/**
 * Whether this declaration CHOOSES between dashboard marks: two alternatives
 * whose marks are each non-empty and neither contained in the other.
 *
 * Containment is what separates the two shapes that look alike. A page that
 * always draws a chart and sometimes adds a KPI row has `{chart}` and
 * `{row, chart}` — one is the other plus an optional extra, and it is one
 * picture with a variation. A renderer that draws a line for one kind and an
 * area for another has `{line}` and `{area}` — neither is the other, and that
 * is the dispatch over visualizations `<Dashboard>` owns.
 *
 * Choosing is the test, not branching and not counting: counting file-wide
 * imports flagged a page that composed two marks side by side, and comparing
 * only top-level returns walked past the same dispatch written one line deeper,
 * inside the JSX it returns.
 */
function dispatchedMarks(
	declaration: ts.Node,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
): string[] {
	const paths = distinct(
		returnedAlternatives(declaration, checker, surface).map(
			(path) => new Set([...path].filter((mark) => DASHBOARD_MARKS.has(mark))),
		),
	).filter((path) => path.size > 0);
	const within = (small: Set<string>, large: Set<string>) =>
		[...small].every((mark) => large.has(mark));
	for (const left of paths) {
		for (const right of paths) {
			if (left === right) continue;
			if (!within(left, right) && !within(right, left)) {
				return [...new Set([...left, ...right])].sort();
			}
		}
	}
	return [];
}

const DISPATCHERS: Record<string, string> = {
	"src/components/dashboard.tsx":
		"the widget-slot renderer: a union of host-supplied widgets, each with its own loading/error/empty state. Goes when the audit page's widgets become a DashboardView and it mounts the kit's renderer.",
	"src/features/dashboard/ui/metric-card.tsx":
		"the dashboard DSL's card: it resolves one declared metric through useMetric and then draws whichever chart the declaration asked for. Goes with the DSL resolving outside the card.",
};

function appSources(): { path: string; source: string }[] {
	return walk(join(appRoot, "src"))
		.map((file) => relative(appRoot, file).replaceAll("\\", "/"))
		.filter(
			(path) =>
				!path.startsWith("src/gen/") && !path.startsWith("src/generated/"),
		)
		.map((path) => ({
			path,
			source: readFileSync(join(appRoot, path), "utf8"),
		}));
}

function kitEntryPoints(): { path: string; source: string }[] {
	return walk(join(appRoot, "packages/codefly-ui/src"))
		.filter((file) => /[/\\]index\.ts$/.test(file))
		.map((file) => ({
			path: relative(appRoot, file).replaceAll("\\", "/"),
			source: readFileSync(file, "utf8"),
		}));
}

function kitNames(): Set<string> {
	return kitExports(kitEntryPoints().map((entry) => entry.source));
}

/** Each host surface's published name → the kit name behind it. */
function hostSurface(): Map<string, string> {
	const surface = new Map<string, string>();
	for (const path of KIT_SURFACES) {
		const file = parse(path, readFileSync(join(appRoot, path), "utf8"));
		for (const statement of file.statements) {
			if (
				!ts.isExportDeclaration(statement) &&
				!ts.isImportDeclaration(statement)
			) {
				continue;
			}
			const specifier = statement.moduleSpecifier;
			if (!specifier || !ts.isStringLiteral(specifier)) continue;
			if (!KIT_SPECIFIER.test(specifier.text)) continue;
			const clause = ts.isExportDeclaration(statement)
				? statement.exportClause
				: statement.importClause?.namedBindings;
			if (!clause) continue;
			if (ts.isNamedExports(clause) || ts.isNamedImports(clause)) {
				for (const element of clause.elements) {
					surface.set(
						element.name.text,
						(element.propertyName ?? element.name).text,
					);
				}
			}
		}
	}
	return surface;
}

function parse(path: string, source: string): ts.SourceFile {
	return ts.createSourceFile(
		path,
		source,
		ts.ScriptTarget.Latest,
		true,
		ts.ScriptKind.TSX,
	);
}

interface Offence {
	name: string;
	path: string;
	/**
	 * Why it is an offence: a name the kit owns, a widget dispatch, or a name
	 * the kit owns published from somewhere this cannot read.
	 */
	kind: "name" | "dispatch" | "unresolved";
}

/**
 * Files whose default export's NAME is fixed by the framework, not chosen:
 * Next's route conventions make every route component a `Page`, every nested
 * shell a `Layout`, and so on, and the kit exports primitives called `Page` and
 * `Layout`. A route component is not a second copy of either — it is the thing
 * that mounts them — so its default export is not read as a declaration taking
 * the kit's name. A NAMED declaration in such a file still is.
 *
 * The root route (`src/app/page.tsx`) counts: it is as conventional as a nested
 * one, and requiring a directory between `app/` and `page.tsx` rejected it.
 */
const ROUTE_CONVENTION =
	/^src\/app\/(?:.*\/)?(page|layout|template|default|error|global-error|loading|not-found|forbidden|unauthorized)\.tsx?$/;

/** What a file's default export resolves to, in every spelling of it. */
function defaultExportNames(
	file: ts.SourceFile,
	checker: ts.TypeChecker,
): Set<string> {
	const names = new Set<string>();
	const moduleSymbol = checker.getSymbolAtLocation(file);
	const exported = moduleSymbol
		? checker
				.getExportsOfModule(moduleSymbol)
				.find((symbol) => symbol.name === "default")
		: undefined;
	for (const symbol of exported ? aliasChain(exported, checker) : []) {
		for (const declaration of symbol.declarations ?? []) {
			if (
				(ts.isFunctionDeclaration(declaration) ||
					ts.isClassDeclaration(declaration) ||
					ts.isVariableDeclaration(declaration)) &&
				declaration.name &&
				ts.isIdentifier(declaration.name)
			) {
				names.add(declaration.name.text);
			}
			if (
				ts.isExportAssignment(declaration) &&
				ts.isIdentifier(declaration.expression)
			) {
				names.add(declaration.expression.text);
			}
		}
	}
	return names;
}

/**
 * Declarations that take a kit export's name without RETURNING the kit's
 * component of that name, names the kit owns that this cannot resolve at all,
 * and declarations that choose between the kit's dashboard marks. Pure, so the
 * fixtures below can exercise it on sources that are not in the tree.
 */
function findOffences(
	files: { path: string; source: string }[],
	kit: Set<string>,
	allowed: Record<string, string> = ALLOWED,
	/** Published name → kit name for the host's own surfaces. */
	surface: Map<string, string> = new Map(),
	dispatchers: Record<string, string> = DISPATCHERS,
): Offence[] {
	const { checker, sources } = programOver(files);
	const offences: Offence[] = [];

	for (const [path, file] of sources) {
		if (KIT_SURFACES.includes(path)) continue;
		const routeDefaults = ROUTE_CONVENTION.test(path)
			? defaultExportNames(file, checker)
			: new Set<string>();
		const components = publishedComponents(file, checker, surface);

		for (const [name, resolution] of components) {
			if (!kit.has(name) || routeDefaults.has(name)) continue;
			if (resolution.kind === "kit" || resolution.kind === "type") continue;
			// An exception is recorded where the component is DECLARED, so a
			// barrel that re-exports it does not need a second one for the
			// same component. The published path is checked too, for a name a
			// file publishes without declaring anywhere in the set.
			const declaredIn =
				resolution.kind === "declaration"
					? resolution.node.getSourceFile().fileName
					: path;
			if (`${name} in ${path}` in allowed) continue;
			if (`${name} in ${declaredIn}` in allowed) continue;
			if (resolution.kind === "unresolved") {
				offences.push({ name, path, kind: "unresolved" });
				continue;
			}
			if (returnedKitComponents(resolution.node, checker, surface).has(name)) {
				continue;
			}
			offences.push({ name, path, kind: "name" });
		}

		if (path in dispatchers) continue;
		for (const [, resolution] of components) {
			if (resolution.kind !== "declaration") continue;
			const marks = dispatchedMarks(resolution.node, checker, surface);
			if (marks.length > 1) {
				offences.push({ name: marks.join("+"), path, kind: "dispatch" });
				break;
			}
		}
	}
	return offences;
}

describe("the host app composes the kit rather than re-implementing it", () => {
	it("declares no component the kit already exports", () => {
		const kit = kitNames();
		expect(
			kit.size,
			"the kit's entry points were not readable, so this gate proves nothing",
		).toBeGreaterThan(100);

		const offences = findOffences(
			appSources(),
			kit,
			ALLOWED,
			hostSurface(),
		).map(({ name, path, kind }) => {
			if (kind === "name") {
				return `${name} in ${path} — @codefly-dev/ui already exports it. Compose the kit's component, or add "${name} in ${path}" to ALLOWED with what the two things are and why neither can stand in for the other.`;
			}
			if (kind === "unresolved") {
				return `${name} in ${path} — @codefly-dev/ui exports that name and this publishes it from somewhere this gate cannot read, so it cannot tell the kit's component from a second one. Publish the kit's own, or declare it here where it can be read.`;
			}
			return `${path} draws with ${name} — more than one of the kit's dashboard marks, which is a widget dispatch and <Dashboard>'s job. Mount <Dashboard> or <DashboardWidget>, or add ${path} to DISPATCHERS with what it is and what would retire it.`;
		});
		expect(offences).toEqual([]);
	});

	it("reads every name the kit exports, star re-exports included", () => {
		// The gate covers what it can parse. A star onto a package outside the
		// kit's own tree is not parsed, so it is refused rather than missed.
		expect(starReExports(kitEntryPoints())).toEqual([
			"packages/codefly-ui/src/plugin-host/index.ts → @codefly-dev/saas-plugin-react",
		]);
	});

	it("cannot build a kit-shaped primitive: the library for building them is not a dependency", () => {
		// The strongest leg, because it is a fact rather than a convention: `cva`
		// is how the kit composes its variants, and it left this package's
		// dependencies with issue #1041. The kit declares it for itself.
		expect(Object.keys(packageJson.dependencies)).not.toContain(
			"class-variance-authority",
		);
		const importers = appSources()
			.filter(({ source }) => /["']class-variance-authority["']/.test(source))
			.map(({ path }) => path);
		expect(importers).toEqual([]);
	});

	it.each(CHART_RE_EXPORTS)(
		"%s stays a re-export of the kit's charts",
		(path) => {
			const source = readFileSync(join(appRoot, path), "utf8");
			expect(source).toContain('from "@codefly-dev/ui/dashboard"');
			// No component of its own: no JSX, no class strings, no variants.
			expect(source).not.toMatch(/\bfunction\b|=>|cva\(|cn\(/);
		},
	);

	it("keeps every entry in ALLOWED a real one", () => {
		const kit = kitNames();
		for (const [key, reason] of Object.entries(ALLOWED)) {
			const [name, path] = key.split(" in ");
			expect(
				kit.has(name),
				`${key}: ${name} is not a kit export any more, so this entry is stale — delete it`,
			).toBe(true);
			expect(
				new RegExp(
					`^(?:export\\s+)?(?:function|const|class)\\s+${name}\\b`,
					"m",
				).test(readFileSync(join(appRoot, path), "utf8")),
				`${key} is not declared there any more, so this entry is stale — delete it`,
			).toBe(true);
			expect(
				reason.length,
				`${key} needs what the two things are and why, not a listing`,
			).toBeGreaterThan(80);
		}
	});
});

// Three directions, because a gate is only as good as what it is known to
// catch, to let through, and to catch when the same duplication arrives by
// another route. Each fixture is a source that is not in the tree, so these
// hold whatever the tree happens to contain.
describe("the detector", () => {
	const kit = new Set(["Card", "CardRoot", "StatTile", "Page", "Badge"]);
	// What the host's surfaces publish, as `src/shared/ui` really does it: one
	// name straight through, one renaming a kit primitive to another kit
	// component's name.
	const surface = new Map([
		["Badge", "Badge"],
		["Card", "CardRoot"],
	]);
	const deletedRenderer = () =>
		readFileSync(
			join(
				appRoot,
				"src/components/__tests__/fixtures/deleted-solution-dashboard.txt",
			),
			"utf8",
		);
	const detect = (path: string, source: string) =>
		findOffences([{ path, source }], kit, {}, surface, {}).map(
			(o) => `${o.name} in ${o.path}`,
		);

	it("catches a component taking a kit export's name", () => {
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`export function Card({ children }: { children: ReactNode }) {
					return <div className="rounded-lg border p-4">{children}</div>;
				}`,
			),
		).toEqual(["Card in src/features/x/ui/card.tsx"]);
	});

	it("lets a wrapper that renders the kit's own component through", () => {
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`import { Card as KitCard } from "@codefly-dev/ui/layout";
				export function Card(props: ComponentProps<typeof KitCard>) {
					return <KitCard {...props} data-analytics="x" />;
				}`,
			),
		).toEqual([]);
	});

	it("catches the same duplication declared module-locally instead of exported", () => {
		// How both duplicates in this tree arrived: not as a new file anyone
		// would review as a component, but as a helper at the bottom of a page.
		// The upstream version of this rule matched only `export function`, and
		// let this shape past.
		expect(
			detect(
				"src/features/x/ui/page.tsx",
				`function Card({ title }: { title: string }) {
					return <div className="rounded-lg border p-4">{title}</div>;
				}
				export function Deliveries() { return <Card title="x" />; }`,
			),
		).toEqual(["Card in src/features/x/ui/page.tsx"]);
	});

	it("catches an arrow-function component, which is the same declaration", () => {
		expect(
			detect(
				"src/features/x/ui/tile.tsx",
				`const StatTile = ({ value }: { value: number }) => <span>{value}</span>;`,
			),
		).toEqual(["StatTile in src/features/x/ui/tile.tsx"]);
	});

	it("catches a declaration that imports a DIFFERENT kit component under the name it takes", () => {
		// Importing `CardRoot` and calling the result `Card` is not a wrapper
		// around `Card`: it is a second `Card` built on a kit primitive. This is
		// how a duplicate escapes a plain name gate — the kit and the consumer
		// call the same thing by different names, so neither name collides.
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`import { CardRoot } from "@codefly-dev/ui/layout";
				export function Card({ title }: { title: string }) {
					return <CardRoot className="p-4 shadow-sm"><h3>{title}</h3></CardRoot>;
				}`,
			),
		).toEqual(["Card in src/features/x/ui/card.tsx"]);
	});

	it("is not fooled by an alias: a wrapper is recognised by the kit's own spelling", () => {
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`import { StatTile as Tile } from "@codefly-dev/ui/dashboard";
				export const StatTile = (props: ComponentProps<typeof Tile>) => <Tile {...props} />;`,
			),
		).toEqual([]);
	});

	it("ignores a re-export, which ships the kit's component rather than another one", () => {
		expect(
			detect(
				"src/components/charts/chart.tsx",
				`export { StatTile, Card } from "@codefly-dev/ui/dashboard";`,
			),
		).toEqual([]);
	});

	it("lets a Next route component keep the name the framework gives it", () => {
		expect(
			detect(
				"src/app/admin/users/page.tsx",
				`export default function Page() { return <Users />; }`,
			),
		).toEqual([]);
	});

	it("still catches a NAMED kit-shaped component inside a route file", () => {
		// The exemption is for the one export whose name Next fixes, not for the
		// file: a second `Card` beside the route component is still a second Card.
		expect(
			detect(
				"src/app/admin/users/page.tsx",
				`function Card({ title }: { title: string }) { return <div>{title}</div>; }
				export default function Page() { return <Card title="x" />; }`,
			),
		).toEqual(["Card in src/app/admin/users/page.tsx"]);
	});

	it("lets a wrapper compose the kit through the host's own import surface", () => {
		// The path a wrapper imports through must not change the verdict. This
		// is the same component, and the same wrapper, as the kit-direct case
		// above; `@/shared/ui` publishes `Badge` straight from the kit.
		expect(
			detect(
				"src/features/x/ui/badge.tsx",
				`import { Badge as KitBadge } from "@/shared/ui";
				export function Badge(props: ComponentProps<typeof KitBadge>) {
					return <KitBadge {...props} data-analytics="x" />;
				}`,
			),
		).toEqual([]);
	});

	it("resolves the surface's alias, so one import is judged one way either way", () => {
		// `@/shared/ui` publishes the kit's `CardRoot` under the name `Card`, so
		// importing `Card` from there composes `CardRoot` — and a declaration
		// named `Card` is a second `Card`, exactly as the kit-direct form of
		// this is caught above. Without resolving the alias the two spellings
		// of one import would get opposite verdicts.
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`import { Card as HostCard } from "@/shared/ui";
				export function Card({ title }: { title: string }) {
					return <HostCard className="p-4 shadow-sm">{title}</HostCard>;
				}`,
			),
		).toEqual(["Card in src/features/x/ui/card.tsx"]);
	});

	it("does not read the host's surfaces themselves as duplicates", () => {
		// A surface file re-exports under a kit name by construction.
		expect(
			detect(
				"src/shared/ui/index.ts",
				`export { CardRoot as Card, Badge } from "@codefly-dev/ui/layout";`,
			),
		).toEqual([]);
	});

	it("catches a same-name copy sitting beside an unrelated kit re-export", () => {
		// The shape a duplicate actually arrives in: somebody re-exports the
		// kit's component for a neighbour and writes their own beside it. A
		// file-wide import check read that re-export as proof of composition
		// and exempted the copy.
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`export { Card as KitCard } from "@codefly-dev/ui/layout";
export const Card = () => <div>copy</div>;`,
			),
		).toEqual(["Card in src/features/x/ui/card.tsx"]);
	});

	it("catches an implementation published under a kit name by an export alias", () => {
		// The declared name is not the shipped name. Reading only declarations
		// let this straight through.
		expect(
			detect(
				"src/features/x/ui/panel.tsx",
				`const LocalPanel = () => <div>copy</div>;
export { LocalPanel as Card };`,
			),
		).toEqual(["Card in src/features/x/ui/panel.tsx"]);
	});

	it("allows a wrapper that reaches the kit through a namespace import", () => {
		expect(
			detect(
				"src/features/x/ui/badge.tsx",
				`import * as UI from "@/shared/ui";
export const Badge = (props) => <UI.Badge {...props} />;`,
			),
		).toEqual([]);
	});

	it("allows a wrapper that reaches a host surface by relative path", () => {
		// `@/shared/ui` and `../../../shared/ui` are the same module; judging
		// only the alias rejected a real wrapper for how it spelled its import.
		expect(
			detect(
				"src/features/x/ui/badge.tsx",
				`import { Badge as KitBadge } from "../../../shared/ui";
export const Badge = (props) => <KitBadge {...props} />;`,
			),
		).toEqual([]);
	});

	it("applies the Next route exception to the ROOT route, not only nested ones", () => {
		expect(
			detect(
				"src/app/page.tsx",
				"export default function Page() { return <main/>; }",
			),
		).toEqual([]);
	});

	it("rejects the exact renderer this gate was written to prevent", () => {
		// The case that shows the name legs are not enough on their own: the
		// renderer #1041 deleted declared WidgetBody, WidgetValues and
		// WidgetCard — not one of them a name the kit exports — so every
		// name-based leg passed it while it was the duplication itself. The
		// dispatch leg is what catches it.
		//
		// Read from a committed fixture, not with `git show`: this suite ships
		// inside an immutable module package, where a consuming repository
		// carries the source tree and not this repository's history, so a
		// fixture reaching for an upstream commit fails there while passing in
		// full-history CI.
		expect(
			detect("src/solutions/SolutionDashboard.tsx", deletedRenderer()).length,
		).toBeGreaterThan(0);
	});

	it("still rejects that renderer when its imports become a namespace", () => {
		// Changing only how the marks are imported, and qualifying the JSX,
		// must not change the verdict: a gate that reads local names rather
		// than what they resolve to is escaped by one edit.
		const named =
			/import \{\s*AreaChart,\s*BarList,\s*LineChart,\s*SortableGrid,\s*StatChart,\s*\} from "@codefly-dev\/ui\/dashboard";/;
		const original = deletedRenderer();
		expect(named.test(original)).toBe(true);
		const namespaced = original
			.replace(named, 'import * as Charts from "@codefly-dev/ui/dashboard";')
			.replace(
				/<(\/?)(AreaChart|BarList|LineChart|SortableGrid|StatChart)\b/g,
				"<$1Charts.$2",
			);
		expect(
			detect("src/solutions/SolutionDashboard.tsx", namespaced).length,
		).toBeGreaterThan(0);
	});

	it("rejects that renderer when its marks are local aliases", () => {
		expect(
			detect(
				"src/features/x/ui/widget.tsx",
				`import { LineChart as L, AreaChart as A } from "@codefly-dev/ui/dashboard";
export function WidgetBody({ kind }) {
	return kind === "line" ? <L points={[]} /> : <A points={[]} />;
}`,
			).length,
		).toBeGreaterThan(0);
	});

	it("does not read a type reference as composition", () => {
		// A parameter typed ComponentProps<typeof KitCard> mentions the kit and
		// draws nothing; reading plain identifiers exempted a Card whose body
		// was a bare <div>.
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`import { Card as KitCard } from "@codefly-dev/ui/layout";
export const Card = (props: React.ComponentProps<typeof KitCard>) => (
	<div>{props.children}</div>
);`,
			),
		).toEqual(["Card in src/features/x/ui/card.tsx"]);
	});

	it("does not let a local shadow stand in for the kit's component", () => {
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card() {
	const KitCard = () => <div />;
	return <KitCard />;
}`,
			),
		).toEqual(["Card in src/features/x/ui/card.tsx"]);
	});

	it("resolves a surface namespace member to the kit name behind it", () => {
		// `@/shared/ui` publishes CardRoot as Card, so <UI.Card> composes
		// CardRoot and a declaration named Card is still a second Card.
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`import * as UI from "@/shared/ui";
export const Card = () => <UI.Card><h3>copy</h3></UI.Card>;`,
			),
		).toEqual(["Card in src/features/x/ui/card.tsx"]);
	});

	it("follows a local alias of a kit import", () => {
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`import { Card as KitCard } from "@codefly-dev/ui/layout";
const Local = KitCard;
export const Card = (props) => <Local {...props} />;`,
			),
		).toEqual([]);
	});

	it("allows a wrapper that renders through a kit namespace", () => {
		expect(
			detect(
				"src/features/x/ui/badge.tsx",
				`import * as UI from "@codefly-dev/ui/layout";
export default function Badge(props) { return <UI.Badge {...props} />; }`,
			),
		).toEqual([]);
	});

	it.each([
		[
			"a default identifier",
			"const Page = () => <main/>; export default Page;",
		],
		[
			"an aliased default",
			"const Page = () => <main/>; export { Page as default };",
		],
	])("applies the route exception to %s", (_name, source) => {
		expect(detect("src/app/page.tsx", source)).toEqual([]);
	});

	it("does not read two marks composed side by side as a dispatch", () => {
		// A KPI row above a chart is a page. What a page may not do is BRANCH
		// between marks, which is the dispatch <Dashboard> owns.
		expect(
			detect(
				"src/features/x/ui/overview.tsx",
				`import { KPIRow, MetricLineChart } from "@codefly-dev/ui/dashboard";
export const Overview = () => (
	<>
		<KPIRow metrics={[]} />
		<MetricLineChart title="t" series={[]} />
	</>
);`,
			),
		).toEqual([]);
	});

	it("catches a copy split across two files by an aliasing re-export", () => {
		expect(
			findOffences(
				[
					{
						path: "src/features/x/ui/local.tsx",
						source: "export const LocalPanel = () => <div>copy</div>;",
					},
					{
						path: "src/features/x/ui/index.ts",
						source: 'export { LocalPanel as Card } from "./local";',
					},
				],
				kit,
				{},
				surface,
				{},
			).length,
		).toBeGreaterThan(0);
	});

	it("lets a page draw ONE kit mark, which is composition and not a dispatch", () => {
		expect(
			detect(
				"src/features/x/ui/page.tsx",
				`import { MetricLineChart } from "@codefly-dev/ui/dashboard";
export const Trend = () => <MetricLineChart title="t" series={[]} />;`,
			),
		).toEqual([]);
	});

	it.each([
		[
			"a guard-return dispatch",
			`import { LineChart as L, AreaChart as A } from "@codefly-dev/ui/dashboard";
export function WidgetBody({ kind }) {
	if (kind === "line") return <L points={[]} />;
	return <A points={[]} />;
}`,
			true,
		],
		[
			"a destructured parameter shadowing the import",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card({ KitCard }) { return <KitCard />; }`,
			true,
		],
		[
			"an unused nested wrapper beside a plain return",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card() {
	const Unused = () => <KitCard />;
	return <div>copy</div>;
}`,
			true,
		],
		[
			"a function-local alias of the kit component",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card(props) {
	const Local = KitCard;
	return <Local {...props} />;
}`,
			false,
		],
		[
			"an alias of a namespace member",
			`import * as UI from "@codefly-dev/ui/layout";
const Local = UI.Card;
export const Card = (props) => <Local {...props} />;`,
			false,
		],
		[
			"a nested shadow that does not reach the outer JSX",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card(props) {
	const helper = (KitCard) => KitCard;
	return <KitCard {...props} />;
}`,
			false,
		],
		[
			"two layout branches drawing the same pair of marks",
			`import { KPIRow, MetricLineChart } from "@codefly-dev/ui/dashboard";
export function Overview({ compact }) {
	return compact ? (
		<section><KPIRow metrics={[]} /><MetricLineChart series={[]} /></section>
	) : (
		<main><KPIRow metrics={[]} /><MetricLineChart series={[]} /></main>
	);
}`,
			false,
		],
	])("%s", (_name, source, rejected) => {
		expect(detect("src/features/x/ui/example.tsx", source).length > 0).toBe(
			rejected,
		);
	});

	it("does not count kit JSX inside a callback the component passes down", () => {
		// A render prop is not what this component returns, and the skip that
		// keeps a nested function out of a returned expression is what says so.
		expect(
			detect(
				"src/features/x/ui/card.tsx",
				`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card() {
	return <Panel render={() => <KitCard />} />;
}`,
			),
		).toEqual(["Card in src/features/x/ui/card.tsx"]);
	});

	it("accepts a genuine wrapper re-exported under the kit's name", () => {
		// Its implementation composes the kit, which is the question — the name
		// it is published under is not evidence either way.
		expect(
			findOffences(
				[
					{
						path: "src/features/x/ui/local.tsx",
						source: `import { Card as KitCard } from "@codefly-dev/ui/layout";
export const LocalPanel = (props) => <KitCard {...props} />;`,
					},
					{
						path: "src/features/x/ui/index.ts",
						source: 'export { LocalPanel as Card } from "./local";',
					},
				],
				kit,
				{},
				surface,
				{},
			),
		).toEqual([]);
	});

	it("catches a copy published under a kit name two hops away", () => {
		expect(
			findOffences(
				[
					{
						path: "src/features/x/ui/local.tsx",
						source: "export const LocalPanel = () => <div>copy</div>;",
					},
					{
						path: "src/features/x/ui/bridge.ts",
						source: 'export { LocalPanel } from "./local";',
					},
					{
						path: "src/features/x/ui/index.ts",
						source: 'export { LocalPanel as Card } from "./bridge";',
					},
				],
				kit,
				{},
				surface,
				{},
			).length,
		).toBeGreaterThan(0);
	});

	// What the gate must see through, and what it must not mistake for a copy.
	// Each of these was found by an independent review of an earlier shape of
	// this detector, and each named a rule rather than a trick: these say what
	// the rule now is.
	it.each([
		[
			"a dispatch written one line inside the JSX it returns",
			`import { LineChart as L, AreaChart as A } from "@codefly-dev/ui/dashboard";
export function WidgetBody({ kind }) {
	return <section>{kind === "line" ? <L points={[]} /> : <A points={[]} />}</section>;
}`,
			true,
		],
		[
			"the same dispatch behind a cast",
			`import { LineChart as L, AreaChart as A } from "@codefly-dev/ui/dashboard";
export function WidgetBody({ kind }) {
	return (kind === "line" ? <L points={[]} /> : <A points={[]} />) as React.ReactNode;
}`,
			true,
		],
		[
			"kit JSX in a comma operand, which is evaluated and discarded",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card() { return (<KitCard />, <div>copy</div>); }`,
			true,
		],
		[
			"kit JSX in a return no execution reaches",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card() {
	return <div>copy</div>;
	return <KitCard />;
}`,
			true,
		],
		[
			"kit JSX handed to another component through a prop",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
function Ignore(props) { return <div>copy</div>; }
export function Card() { return <Ignore unused={<KitCard />} />; }`,
			true,
		],
		[
			"a dispatch whose arms are fragments",
			`import { LineChart as L, AreaChart as A } from "@codefly-dev/ui/dashboard";
export function WidgetBody({ kind }) {
	return kind === "line" ? <><L points={[]} /></> : <><A points={[]} /></>;
}`,
			true,
		],
		[
			"kit JSX returned by a nested function the component only declares",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card() {
	function Unused() { return <KitCard />; }
	return <div>copy</div>;
}`,
			true,
		],
		[
			"kit JSX in a callback the component puts in its children",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card() { return <Panel>{() => <KitCard />}</Panel>; }`,
			true,
		],
		[
			"returned kit JSX in an array, which React renders",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card(props) { return [<KitCard key="only" {...props} />]; }`,
			false,
		],
		[
			"returned kit JSX held in a local first",
			`import { Card as KitCard } from "@codefly-dev/ui/layout";
export function Card(props) {
	const content = <KitCard {...props} />;
	return content;
}`,
			false,
		],
		[
			"a mark drawn always beside one drawn only sometimes",
			`import { KPIRow, MetricLineChart } from "@codefly-dev/ui/dashboard";
export function Overview({ withRow }) {
	return (
		<section>
			{withRow ? <KPIRow metrics={[]} /> : null}
			<MetricLineChart series={[]} />
		</section>
	);
}`,
			false,
		],
	])("%s", (_name, source, rejected) => {
		expect(detect("src/features/x/ui/example.tsx", source).length > 0).toBe(
			rejected,
		);
	});

	it("catches a copy re-exported through the app's own path alias", () => {
		// `@/…` and `./…` reach the same module. Resolving only the second read
		// the barrel and stopped, so the copy at the end of it went unexamined.
		expect(
			findOffences(
				[
					{
						path: "src/features/x/ui/local.tsx",
						source: "export const LocalPanel = () => <div>copy</div>;",
					},
					{
						path: "src/features/x/ui/index.ts",
						source:
							'export { LocalPanel as Card } from "@/features/x/ui/local";',
					},
				],
				kit,
				{},
				surface,
				{},
			).map(({ name, kind }) => `${kind}:${name}`),
			// `name`, not `unresolved`: the copy was READ. An alias the gate
			// cannot follow would also be refused, but for not knowing, and that
			// is a weaker answer than the one available here.
		).toEqual(["name:Card"]);
	});

	it("catches a copy however many re-exports stand between", () => {
		// A chain is followed to its end, cycle-detected rather than cut off at
		// a depth: ten hops is not more suspicious than two, and a gate that
		// gave up at eight could be escaped by writing a ninth.
		const sources = [
			{
				path: "src/features/x/ui/local.tsx",
				source: "export const LocalPanel = () => <div>copy</div>;",
			},
		];
		for (let hop = 0; hop < 9; hop += 1) {
			sources.push({
				path: `src/features/x/ui/bridge${hop}.ts`,
				source: `export { LocalPanel } from "./${hop ? `bridge${hop - 1}` : "local"}";`,
			});
		}
		sources.push({
			path: "src/features/x/ui/index.ts",
			source: 'export { LocalPanel as Card } from "./bridge8";',
		});
		expect(
			findOffences(sources, kit, {}, surface, {}).map(
				({ name, kind }) => `${kind}:${name}`,
			),
		).toEqual(["name:Card"]);
	});

	it.each([
		[
			"a named import",
			'export { Card as Panel } from "@codefly-dev/ui/layout";',
			'import { Panel } from "./bridge";\nexport const Card = (props) => <Panel {...props} />;',
		],
		[
			"a namespace import",
			'export { Card } from "@codefly-dev/ui/layout";',
			'import * as UI from "./bridge";\nexport const Card = (props) => <UI.Card {...props} />;',
		],
	])(
		"accepts a wrapper reaching the kit through the app's own barrel by %s",
		(_name, bridge, local) => {
			// The barrel is the app's, the component behind it is the kit's. A
			// gate that stopped at the barrel rejected a genuine wrapper for how
			// it spelled its import.
			expect(
				findOffences(
					[
						{ path: "src/features/x/ui/bridge.ts", source: bridge },
						{ path: "src/features/x/ui/local.tsx", source: local },
					],
					kit,
					{},
					surface,
					{},
				),
			).toEqual([]);
		},
	);

	it("refuses a kit name published from outside anything it can read", () => {
		// Not a hand-written copy and not the kit either: a second Card from a
		// third-party package is the same two-renderers problem, and "could not
		// tell" is not a verdict this gate is allowed to reach.
		expect(
			findOffences(
				[
					{
						path: "src/features/x/ui/index.ts",
						source: 'export { Panel as Card } from "some-other-ui-kit";',
					},
				],
				kit,
				{},
				surface,
				{},
			).length,
		).toBeGreaterThan(0);
	});

	it("does not refuse a TYPE that shares a kit name", () => {
		// A type cannot be rendered and cannot stand in for a component, so it
		// is neither a copy nor something unreadable.
		expect(
			findOffences(
				[
					{
						path: "src/features/x/model/types.ts",
						source: "export interface Card { id: string }",
					},
					{
						path: "src/features/x/ui/index.ts",
						source: 'export type { Card } from "../model/types";',
					},
				],
				kit,
				{},
				surface,
				{},
			),
		).toEqual([]);
	});

	it("ignores a name the kit does not export", () => {
		expect(
			detect(
				"src/features/x/ui/panel.tsx",
				`export function PayloadBlock({ title }: { title: string }) { return <div>{title}</div>; }`,
			),
		).toEqual([]);
	});
});
