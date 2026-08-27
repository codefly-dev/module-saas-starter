import type * as React from "react";

import { cn } from "@/lib/utils";

// Composable layout primitives. Pages compose these instead of hand-rolling the
// same Tailwind utilities on every screen:
//
//   <Layout>
//     <Page>
//       <PageHeader title="Users" actions={<Button>Invite</Button>} />
//       <Grid cols={3}>…</Grid>
//     </Page>
//   </Layout>

// Layout constrains page content to a readable max width and centers it. It does
// not add its own padding: the route shell that renders these pages owns the
// page gutters (AdminLayout's <main> is `p-6`), so applying padding here too
// would double it. Pass `className` for gutters when using Layout outside a
// padded shell.
function Layout({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="layout"
			className={cn("mx-auto w-full max-w-7xl", className)}
			{...props}
		/>
	);
}

// Page stacks a screen's sections with consistent vertical rhythm. Compose it
// inside a feature component (e.g. UsersPage), not a route `page.tsx` — the
// route file's `export default function Page()` would shadow this import. Alias
// it (`import { Page as PageBody }`) if you must use both in one file.
function Page({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div data-slot="page" className={cn("space-y-6", className)} {...props} />
	);
}

// PageHeader renders a page title, optional description, and a trailing actions
// slot. Pass `children` for supplementary header content (search, filters).
function PageHeader({
	title,
	description,
	actions,
	className,
	children,
	...props
}: Omit<React.ComponentProps<"div">, "title"> & {
	title?: React.ReactNode;
	description?: React.ReactNode;
	actions?: React.ReactNode;
}) {
	return (
		<div
			data-slot="page-header"
			className={cn("flex items-start justify-between gap-4", className)}
			{...props}
		>
			<div className="space-y-1">
				{title != null && (
					<h1 className="text-2xl font-bold tracking-tight">{title}</h1>
				)}
				{description != null && (
					<p className="text-muted-foreground">{description}</p>
				)}
				{children}
			</div>
			{actions != null && (
				<div className="flex shrink-0 items-center gap-2">{actions}</div>
			)}
		</div>
	);
}

const gridCols: Record<1 | 2 | 3 | 4, string> = {
	1: "grid-cols-1",
	2: "grid-cols-1 sm:grid-cols-2",
	3: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-3",
	4: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-4",
};

const gapClass: Record<2 | 3 | 4 | 6 | 8, string> = {
	2: "gap-2",
	3: "gap-3",
	4: "gap-4",
	6: "gap-6",
	8: "gap-8",
};

// Grid lays children out in a responsive column grid that collapses to a single
// column on small screens.
function Grid({
	cols = 1,
	gap = 4,
	className,
	...props
}: React.ComponentProps<"div"> & {
	cols?: 1 | 2 | 3 | 4;
	gap?: 2 | 3 | 4 | 6 | 8;
}) {
	return (
		<div
			data-slot="grid"
			className={cn("grid", gridCols[cols], gapClass[gap], className)}
			{...props}
		/>
	);
}

const stackAlign = {
	start: "items-start",
	center: "items-center",
	end: "items-end",
	stretch: "items-stretch",
} as const;

const stackJustify = {
	start: "justify-start",
	center: "justify-center",
	end: "justify-end",
	between: "justify-between",
} as const;

// Stack arranges children in a flex column (default) or row with a uniform gap.
function Stack({
	direction = "col",
	gap = 4,
	align,
	justify,
	className,
	...props
}: React.ComponentProps<"div"> & {
	direction?: "row" | "col";
	gap?: 2 | 3 | 4 | 6 | 8;
	align?: keyof typeof stackAlign;
	justify?: keyof typeof stackJustify;
}) {
	return (
		<div
			data-slot="stack"
			className={cn(
				"flex",
				direction === "row" ? "flex-row" : "flex-col",
				gapClass[gap],
				align && stackAlign[align],
				justify && stackJustify[justify],
				className,
			)}
			{...props}
		/>
	);
}

// Section groups related content under an optional heading and actions row.
function Section({
	title,
	description,
	actions,
	className,
	children,
	...props
}: Omit<React.ComponentProps<"section">, "title"> & {
	title?: React.ReactNode;
	description?: React.ReactNode;
	actions?: React.ReactNode;
}) {
	const hasHeader = title != null || description != null || actions != null;
	return (
		<section
			data-slot="section"
			className={cn("space-y-4", className)}
			{...props}
		>
			{hasHeader && (
				<div className="flex items-start justify-between gap-4">
					<div className="space-y-1">
						{title != null && (
							<h2 className="text-lg font-semibold tracking-tight">{title}</h2>
						)}
						{description != null && (
							<p className="text-sm text-muted-foreground">{description}</p>
						)}
					</div>
					{actions != null && (
						<div className="flex shrink-0 items-center gap-2">{actions}</div>
					)}
				</div>
			)}
			{children}
		</section>
	);
}

// Panel is a bordered surface for grouping content inside a section or grid cell.
function Panel({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="panel"
			className={cn(
				"rounded-lg border bg-card p-4 text-card-foreground",
				className,
			)}
			{...props}
		/>
	);
}

export { Grid, Layout, Page, PageHeader, Panel, Section, Stack };
