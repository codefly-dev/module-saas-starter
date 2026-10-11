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
 * The kit components a file has a binding for, as local-name → kit-name, with
 * a namespace import recorded as `*`.
 *
 * Resolved from the AST rather than by regex, because a declaration's own body
 * has to be checked against these bindings: file-wide import presence proved
 * nothing. An unrelated `export { Card as KitCard } from "@codefly-dev/ui"`
 * was enough to exempt a hand-rolled `Card` sitting beside it, which is the
 * shape a duplicate actually arrives in — somebody re-exports the kit's thing
 * for a neighbour and writes their own in the same file.
 */
function kitBindings(
	file: ts.SourceFile,
	path: string,
	surface: Map<string, string>,
): Map<string, string> {
	const bindings = new Map<string, string>();
	for (const statement of file.statements) {
		if (!ts.isImportDeclaration(statement)) continue;
		if (!ts.isStringLiteral(statement.moduleSpecifier)) continue;
		const specifier = statement.moduleSpecifier.text;
		const kit = KIT_SPECIFIER.test(specifier);
		const host = isHostSurface(specifier, path);
		if (!kit && !host) continue;
		const clause = statement.importClause;
		if (!clause) continue;
		// `import * as UI from "@/shared/ui"` — every member access on it is a
		// kit component, so the namespace itself is the binding.
		if (clause.namedBindings && ts.isNamespaceImport(clause.namedBindings)) {
			bindings.set(clause.namedBindings.name.text, "*");
			continue;
		}
		if (!clause.namedBindings || !ts.isNamedImports(clause.namedBindings)) {
			continue;
		}
		for (const element of clause.namedBindings.elements) {
			const published = (element.propertyName ?? element.name).text;
			// Through a host surface the published name is that file's, so it is
			// resolved back to the kit's own: `src/shared/ui` re-exports
			// `CardRoot` as `Card`, so importing `Card` from there composes
			// `CardRoot`, and a declaration named `Card` beside it is still a
			// second `Card`.
			const kitName = kit ? published : surface.get(published);
			if (kitName) bindings.set(element.name.text, kitName);
		}
	}
	return bindings;
}

/**
 * Every component this file declares, by the name it is PUBLISHED under, with
 * the declaration's own node.
 *
 * The published name is what matters, and it is not always the declared one:
 * `const LocalPanel = …; export { LocalPanel as Card }` ships a `Card`, and
 * reading only declaration names let that straight through.
 */
function declarations(file: ts.SourceFile): Map<string, ts.Node> {
	const declared = new Map<string, ts.Node>();
	const published = new Map<string, ts.Node>();
	const visit = (node: ts.Node) => {
		if (ts.isFunctionDeclaration(node) && node.name) {
			declared.set(node.name.text, node);
		} else if (ts.isClassDeclaration(node) && node.name) {
			declared.set(node.name.text, node);
		} else if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name)) {
			declared.set(node.name.text, node);
		}
		ts.forEachChild(node, visit);
	};
	visit(file);
	// `export { Local as Published }` with no `from`: a local declaration
	// published under another name.
	for (const statement of file.statements) {
		if (!ts.isExportDeclaration(statement) || statement.moduleSpecifier) {
			continue;
		}
		const clause = statement.exportClause;
		if (!clause || !ts.isNamedExports(clause)) continue;
		for (const element of clause.elements) {
			const local = (element.propertyName ?? element.name).text;
			const node = declared.get(local);
			if (node) published.set(element.name.text, node);
		}
	}
	const all = new Map<string, ts.Node>();
	for (const [name, node] of declared) {
		if (/^[A-Z][A-Za-z0-9]*$/.test(name)) all.set(name, node);
	}
	for (const [name, node] of published) {
		if (/^[A-Z][A-Za-z0-9]*$/.test(name)) all.set(name, node);
	}
	return all;
}

/** Whether this declaration's own body renders the kit component `kitName`. */
function composesWithin(
	node: ts.Node,
	kitName: string,
	bindings: Map<string, string>,
): boolean {
	let found = false;
	const visit = (child: ts.Node) => {
		if (found) return;
		if (ts.isIdentifier(child) && bindings.get(child.text) === kitName) {
			found = true;
			return;
		}
		// `UI.Badge` off a namespace import.
		if (
			ts.isPropertyAccessExpression(child) &&
			ts.isIdentifier(child.expression) &&
			bindings.get(child.expression.text) === "*" &&
			child.name.text === kitName
		) {
			found = true;
			return;
		}
		ts.forEachChild(child, visit);
	};
	ts.forEachChild(node, visit);
	return found;
}

/**
 * The kit's dashboard marks. A host surface may compose ONE of these — a page
 * drawing a chart — but a file that reaches for several is dispatching over a
 * widget's visualization, which is `<Dashboard>`'s whole job.
 *
 * This is the leg that catches a duplicate under a NEW name, which no
 * name-based rule can: the renderer #1041 deleted declared `WidgetBody`,
 * `WidgetValues` and `WidgetCard` — not one of them a name the kit exports —
 * and so produced zero offences from the legs above while being the exact
 * duplication this file exists to prevent.
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
 * Files that draw with more than one of the kit's marks and may keep doing so,
 * with what each is and what would retire it. They are the host's older
 * dashboard renderers, the same ones {@link ALLOWED} names; this list exists so
 * a NEW one cannot join them quietly.
 */
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

/** The names a file's default export declares, to exempt a route component. */
function defaultExportNames(file: ts.SourceFile): Set<string> {
	const names = new Set<string>();
	for (const statement of file.statements) {
		const isDefault = ts
			.getModifiers(statement as ts.HasModifiers)
			?.some((modifier) => modifier.kind === ts.SyntaxKind.DefaultKeyword);
		if (!isDefault) continue;
		if (
			(ts.isFunctionDeclaration(statement) ||
				ts.isClassDeclaration(statement)) &&
			statement.name
		) {
			names.add(statement.name.text);
		}
	}
	return names;
}

/**
 * Declarations that take a kit export's name without composing the kit's
 * component of that name WITHIN their own body, plus files that dispatch over
 * more than one of the kit's dashboard marks. Pure, so the fixtures below can
 * exercise it on sources that are not in the tree.
 */
function findOffences(
	files: { path: string; source: string }[],
	kit: Set<string>,
	allowed: Record<string, string> = ALLOWED,
	/** Published name → kit name for the host's own surfaces. */
	surface: Map<string, string> = new Map(),
	dispatchers: Record<string, string> = DISPATCHERS,
): Offence[] {
	const offences: Offence[] = [];
	for (const { path, source } of files) {
		if (KIT_SURFACES.includes(path)) continue;
		const file = parse(path, source);
		const bindings = kitBindings(file, path, surface);
		const routeDefaults = ROUTE_CONVENTION.test(path)
			? defaultExportNames(file)
			: new Set<string>();

		for (const [name, node] of declarations(file)) {
			if (!kit.has(name) || routeDefaults.has(name)) continue;
			if (composesWithin(node, name, bindings)) continue;
			if (`${name} in ${path}` in allowed) continue;
			offences.push({ name, path, kind: "name" });
		}

		if (!(path in dispatchers)) {
			const marks = new Set<string>();
			for (const kitName of bindings.values()) {
				if (DASHBOARD_MARKS.has(kitName)) marks.add(kitName);
			}
			if (marks.size > 1) {
				offences.push({
					name: [...marks].sort().join("+"),
					path,
					kind: "dispatch",
				});
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

	it("rejects the exact renderer this gate was written to prevent", async () => {
		// The case that shows the name legs are not enough on their own: the
		// renderer #1041 deleted declared WidgetBody, WidgetValues and
		// WidgetCard — not one of them a name the kit exports — so every
		// name-based leg passed it while it was the duplication itself. The
		// dispatch leg is what catches it, and this reads the real deleted
		// source rather than a paraphrase of it.
		const { execFileSync } = await import("node:child_process");
		const deleted = execFileSync(
			"git",
			[
				"show",
				"8590d329:module/services/frontend/code/src/solutions/SolutionDashboard.tsx",
			],
			{ encoding: "utf8", cwd: appRoot },
		);
		expect(
			detect("src/solutions/SolutionDashboard.tsx", deleted).length,
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

	it("ignores a name the kit does not export", () => {
		expect(
			detect(
				"src/features/x/ui/panel.tsx",
				`export function PayloadBlock({ title }: { title: string }) { return <div>{title}</div>; }`,
			),
		).toEqual([]);
	});
});
