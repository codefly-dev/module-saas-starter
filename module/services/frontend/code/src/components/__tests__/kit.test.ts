import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
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
 * The names a file imports FROM the kit, under the kit's own spelling: the `X`
 * of `import { X as KitX } from "@codefly-dev/ui/layout"`. A wrapper is
 * recognised by this, not by its alias, so renaming the alias cannot turn a
 * re-implementation into a wrapper.
 */
function kitImports(source: string): Set<string> {
	const imported = new Set<string>();
	const from =
		/import\s+(?:type\s+)?\{([^}]*)\}\s*from\s*["']@codefly-dev\/(?:ui|saas-ui)(?:\/[\w./-]+)?["']/g;
	for (const block of source.matchAll(from)) {
		for (const raw of block[1].split(",")) {
			const name = raw
				.trim()
				.replace(/^type\s+/, "")
				.split(/\s+as\s+/)[0]
				.trim();
			if (name) imported.add(name);
		}
	}
	return imported;
}

/**
 * A component declaration, exported or module-local, at any indentation — a
 * duplicate nested inside another component is still a duplicate, and the
 * commonest hiding place for one.
 */
const DECLARATION =
	/^[ \t]*(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:function|const|let|class)\s+([A-Z][A-Za-z0-9]*)/gm;

/**
 * Files whose default export's NAME is fixed by the framework, not chosen:
 * Next's route conventions make every route component a `Page`, every nested
 * shell a `Layout`, and so on, and the kit exports primitives called `Page` and
 * `Layout`. A route component is not a second copy of either — it is the thing
 * that mounts them — so its default export is not read as a declaration taking
 * the kit's name. A NAMED declaration in such a file still is.
 */
const ROUTE_CONVENTION =
	/^src\/app\/.*\/(page|layout|template|default|error|global-error|loading|not-found|forbidden|unauthorized)\.tsx?$/;
const DEFAULT_EXPORT = /^[ \t]*export\s+default\s/;

interface Offence {
	name: string;
	path: string;
}

/**
 * Declarations in these files that take a kit export's name without composing
 * the kit's component of that name. Pure, so the fixtures below can exercise it
 * on sources that are not in the tree.
 */
function findOffences(
	files: { path: string; source: string }[],
	kit: Set<string>,
	allowed: Record<string, string> = ALLOWED,
): Offence[] {
	const offences: Offence[] = [];
	for (const { path, source } of files) {
		const composed = kitImports(source);
		const routeFile = ROUTE_CONVENTION.test(path);
		const seen = new Set<string>();
		for (const declaration of source.matchAll(DECLARATION)) {
			const name = declaration[1];
			if (!kit.has(name) || seen.has(name)) continue;
			if (routeFile && DEFAULT_EXPORT.test(declaration[0])) continue;
			seen.add(name);
			if (composed.has(name)) continue;
			if (`${name} in ${path}` in allowed) continue;
			offences.push({ name, path });
		}
	}
	return offences;
}

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

function kitNames(): Set<string> {
	const entries = walk(join(appRoot, "packages/codefly-ui/src"))
		.filter((file) => /[/\\]index\.ts$/.test(file))
		.map((file) => readFileSync(file, "utf8"));
	return kitExports(entries);
}

describe("the host app composes the kit rather than re-implementing it", () => {
	it("declares no component the kit already exports", () => {
		const kit = kitNames();
		expect(
			kit.size,
			"the kit's entry points were not readable, so this gate proves nothing",
		).toBeGreaterThan(100);

		const offences = findOffences(appSources(), kit).map(
			({ name, path }) =>
				`${name} in ${path} — @codefly-dev/ui already exports it. Compose the kit's component, or add "${name} in ${path}" to ALLOWED with what the two things are and why neither can stand in for the other.`,
		);
		expect(offences).toEqual([]);
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
	const kit = new Set(["Card", "CardRoot", "StatTile", "Page"]);
	const detect = (path: string, source: string) =>
		findOffences([{ path, source }], kit, {}).map(
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

	it("ignores a name the kit does not export", () => {
		expect(
			detect(
				"src/features/x/ui/panel.tsx",
				`export function PayloadBlock({ title }: { title: string }) { return <div>{title}</div>; }`,
			),
		).toEqual([]);
	});
});
