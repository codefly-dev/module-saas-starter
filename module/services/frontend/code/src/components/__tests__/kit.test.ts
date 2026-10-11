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
 * What the gate cannot do, said plainly: a duplicate under a NEW name, built by
 * hand, passes all three legs. {@link ALLOWED} is the backstop for that — a
 * label a reviewer reads and asks about, not a detector. Classifying files by
 * their names instead would fail on the next file anyone adds, and a gate that
 * cries wolf is a gate that gets deleted.
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
				if (!name.startsWith(".")) return undefined;
				const base = join(dirname(containingFile), name).replaceAll("\\", "/");
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

/** The import declaration a symbol's declaration sits under, if any. */
function importOf(declaration: ts.Node): ts.ImportDeclaration | undefined {
	for (let node: ts.Node | undefined = declaration; node; node = node.parent) {
		if (ts.isImportDeclaration(node)) return node;
	}
	return undefined;
}

/**
 * The kit component an identifier refers to, by the kit's OWN name, or
 * `"*namespace"` for a whole kit module, or undefined for anything else —
 * including anything the checker cannot resolve, which is the conservative
 * answer: unresolved is not composition.
 *
 * Follows what the compiler follows: import aliases, local variable aliases
 * (`const Local = KitCard`, `const Local = UI.Card`) and export chains of any
 * length.
 */
function kitComponentOf(
	node: ts.Expression | ts.JsxTagNameExpression,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
	seen = new Set<ts.Node>(),
): string | undefined {
	if (seen.has(node)) return undefined;
	seen.add(node);

	// `UI.Card` — resolve the namespace, then the member through the surface.
	if (ts.isPropertyAccessExpression(node)) {
		const base = kitComponentOf(node.expression, checker, surface, seen);
		if (base === KIT_NAMESPACE) return node.name.text;
		if (base === SURFACE_NAMESPACE) return surface.get(node.name.text);
		return undefined;
	}
	if (!ts.isIdentifier(node)) return undefined;

	let symbol = checker.getSymbolAtLocation(node);
	if (!symbol) return undefined;
	// Through import and export aliases, however many hops.
	for (let hop = 0; hop < 8 && symbol.flags & ts.SymbolFlags.Alias; hop += 1) {
		const target = checker.getImmediateAliasedSymbol(symbol);
		const declaration = symbol.declarations?.[0];
		// An import of a kit module is where the chain ends: the module is not
		// in the program, so there is nothing further to resolve, and the
		// import declaration in the source is the evidence.
		if (declaration) {
			const importDeclaration = importOf(declaration);
			if (
				importDeclaration &&
				ts.isStringLiteral(importDeclaration.moduleSpecifier)
			) {
				const specifier = importDeclaration.moduleSpecifier.text;
				const fromKit = KIT_SPECIFIER.test(specifier);
				const fromSurface = isHostSurface(
					specifier,
					importDeclaration.getSourceFile().fileName,
				);
				if (fromKit || fromSurface) {
					if (ts.isNamespaceImport(declaration)) {
						return fromKit ? KIT_NAMESPACE : SURFACE_NAMESPACE;
					}
					if (ts.isImportSpecifier(declaration)) {
						if (declaration.isTypeOnly) return undefined;
						const published = (declaration.propertyName ?? declaration.name)
							.text;
						return fromKit ? published : surface.get(published);
					}
					return undefined;
				}
			}
		}
		if (!target) break;
		symbol = target;
	}

	// A local alias: follow its initializer.
	const declaration = symbol.declarations?.[0];
	if (
		declaration &&
		ts.isVariableDeclaration(declaration) &&
		declaration.initializer
	) {
		return kitComponentOf(declaration.initializer, checker, surface, seen);
	}
	return undefined;
}

const KIT_NAMESPACE = "*kit";
const SURFACE_NAMESPACE = "*surface";

/**
 * The kit components a declaration RETURNS, as the sets reachable by each
 * distinct return path.
 *
 * Two things this must not do. It must not count JSX in a nested function —
 * an unused local wrapper is not what this component renders. And it must not
 * flatten the paths, because the difference between a page and a dispatch is
 * whether the arms render DIFFERENT marks: a layout that draws the same pair
 * inside a `<section>` or a `<main>` is one picture twice, while choosing a
 * line or an area by a widget's kind is a dispatch.
 */
function returnedPaths(
	declaration: ts.Node,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
): Set<string>[] {
	const body = functionBodyOf(declaration);
	if (!body) return [];

	const marksIn = (root: ts.Node): Set<string> => {
		const found = new Set<string>();
		const visit = (node: ts.Node) => {
			// A nested function is not what this component returns.
			if (isFunctionLike(node) && node !== root) return;
			if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
				const kit = kitComponentOf(node.tagName, checker, surface);
				if (kit && !kit.startsWith("*")) found.add(kit);
			}
			ts.forEachChild(node, visit);
		};
		visit(root);
		return found;
	};

	// Each return path separately: every `return` in this body (not in a
	// nested function), and each arm of a conditional inside one.
	const paths: Set<string>[] = [];
	const addExpression = (expression: ts.Expression) => {
		if (ts.isConditionalExpression(expression)) {
			addExpression(expression.whenTrue);
			addExpression(expression.whenFalse);
			return;
		}
		if (ts.isParenthesizedExpression(expression)) {
			addExpression(expression.expression);
			return;
		}
		paths.push(marksIn(expression));
	};
	if (!ts.isBlock(body)) {
		// A concise arrow body is the returned expression.
		addExpression(body as ts.Expression);
		return paths;
	}
	const visit = (node: ts.Node) => {
		if (isFunctionLike(node) && node !== body) return;
		if (ts.isReturnStatement(node) && node.expression) {
			addExpression(node.expression);
			return;
		}
		ts.forEachChild(node, visit);
	};
	visit(body);
	return paths;
}

function isFunctionLike(node: ts.Node): boolean {
	return (
		ts.isFunctionDeclaration(node) ||
		ts.isFunctionExpression(node) ||
		ts.isArrowFunction(node) ||
		ts.isMethodDeclaration(node)
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

/** Every kit component this declaration returns, flattened. */
function returnedKitComponents(
	declaration: ts.Node,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
): Set<string> {
	return new Set(
		returnedPaths(declaration, checker, surface).flatMap((path) => [...path]),
	);
}

/**
 * Every component a file publishes, by published name, with the declaration
 * that implements it — following an export chain of any length into whichever
 * file actually declares it, so splitting a copy from its kit-named export
 * hides nothing and a genuine wrapper re-exported under the kit's name is not
 * mistaken for one.
 */
function publishedComponents(
	file: ts.SourceFile,
	checker: ts.TypeChecker,
): Map<string, ts.Node> {
	const published = new Map<string, ts.Node>();
	const implementation = (
		symbol: ts.Symbol | undefined,
	): ts.Node | undefined => {
		let resolved = symbol;
		for (let hop = 0; resolved && hop < 8; hop += 1) {
			const declaration = resolved.declarations?.[0];
			if (!declaration) return undefined;
			if (
				ts.isFunctionDeclaration(declaration) ||
				ts.isClassDeclaration(declaration) ||
				ts.isVariableDeclaration(declaration)
			) {
				return declaration;
			}
			if (!(resolved.flags & ts.SymbolFlags.Alias)) return undefined;
			resolved = checker.getImmediateAliasedSymbol(resolved);
		}
		return undefined;
	};

	const moduleSymbol = checker.getSymbolAtLocation(file);
	if (moduleSymbol) {
		for (const exported of checker.getExportsOfModule(moduleSymbol)) {
			if (!/^[A-Z][A-Za-z0-9]*$/.test(exported.name)) continue;
			const node = implementation(exported);
			if (node) published.set(exported.name, node);
		}
	}
	// Declarations the file does not export are still declarations here.
	const visit = (node: ts.Node) => {
		const name =
			(ts.isFunctionDeclaration(node) || ts.isClassDeclaration(node)) &&
			node.name
				? node.name.text
				: ts.isVariableDeclaration(node) && ts.isIdentifier(node.name)
					? node.name.text
					: undefined;
		if (name && /^[A-Z][A-Za-z0-9]*$/.test(name) && !published.has(name)) {
			published.set(name, node);
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
 * Whether this declaration chooses between dashboard marks — two return paths
 * that render DIFFERENT sets of them.
 *
 * Differing is the test, not branching and not counting. A layout that returns
 * the same pair inside a `<section>` or a `<main>` has two paths and one
 * picture, and must pass; a component that returns a line for one kind and an
 * area for another has two pictures, and is the dispatch `<Dashboard>` owns.
 * Both the `cond ? … : …` and the guard-return spellings are return paths, so
 * neither needs its own rule.
 */
function dispatchedMarks(
	declaration: ts.Node,
	checker: ts.TypeChecker,
	surface: Map<string, string>,
): string[] {
	const paths = returnedPaths(declaration, checker, surface)
		.map(
			(path) => new Set([...path].filter((mark) => DASHBOARD_MARKS.has(mark))),
		)
		.filter((path) => path.size > 0);
	const shapes = new Set(paths.map((path) => [...path].sort().join("+")));
	if (shapes.size < 2) return [];
	return [...new Set(paths.flatMap((path) => [...path]))].sort();
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
	/** Why it is an offence: a name the kit owns, or a widget dispatch. */
	kind: "name" | "dispatch";
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
	let symbol = exported;
	for (let hop = 0; symbol && hop < 8; hop += 1) {
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
		if (!(symbol.flags & ts.SymbolFlags.Alias)) break;
		symbol = checker.getImmediateAliasedSymbol(symbol);
	}
	return names;
}

/**
 * Declarations that take a kit export's name without RETURNING the kit's
 * component of that name, plus declarations that choose between the kit's
 * dashboard marks. Pure, so the fixtures below can exercise it on sources that
 * are not in the tree.
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
		const components = publishedComponents(file, checker);

		for (const [name, declaration] of components) {
			if (!kit.has(name) || routeDefaults.has(name)) continue;
			if (returnedKitComponents(declaration, checker, surface).has(name)) {
				continue;
			}
			// An exception is recorded where the component is DECLARED, so a
			// barrel that re-exports it does not need a second one for the
			// same component. The published path is checked too, for a name a
			// file publishes without declaring anywhere in the set.
			const declaredIn = declaration.getSourceFile().fileName;
			if (`${name} in ${path}` in allowed) continue;
			if (`${name} in ${declaredIn}` in allowed) continue;
			offences.push({ name, path, kind: "name" });
		}

		if (path in dispatchers) continue;
		for (const [, declaration] of components) {
			const marks = dispatchedMarks(declaration, checker, surface);
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
		).map(({ name, path, kind }) =>
			kind === "name"
				? `${name} in ${path} — @codefly-dev/ui already exports it. Compose the kit's component, or add "${name} in ${path}" to ALLOWED with what the two things are and why neither can stand in for the other.`
				: `${path} draws with ${name} — more than one of the kit's dashboard marks, which is a widget dispatch and <Dashboard>'s job. Mount <Dashboard> or <DashboardWidget>, or add ${path} to DISPATCHERS with what it is and what would retire it.`,
		);
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

	it("ignores a name the kit does not export", () => {
		expect(
			detect(
				"src/features/x/ui/panel.tsx",
				`export function PayloadBlock({ title }: { title: string }) { return <div>{title}</div>; }`,
			),
		).toEqual([]);
	});
});
