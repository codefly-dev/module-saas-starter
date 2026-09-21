import {
	DEFAULT_CONTROL_SIZES,
	DEFAULT_FRONTEND_APPEARANCE,
	DEFAULT_TYPE_ROLES,
	DEFAULT_TYPE_SCALE,
	DEFAULT_TYPE_SLOTS,
	FRONTEND_CONTROL_SIZE_NAMES,
	FRONTEND_TYPE_ROLE_NAMES,
	FRONTEND_TYPE_SCALE_STEPS,
	FRONTEND_TYPE_SLOT_NAMES,
	resolveTypeSlot,
} from "@codefly/saas-plugin-contract";
import { useState } from "react";

import {
	Button,
	Field,
	Input,
	Pagination,
	SegmentedControl,
	Table,
	TableBody,
	TableCell,
	TableEmptyState,
	TableHead,
	TableHeader,
	TableRow,
	TableToolbar,
} from "../src/layout/index.js";

// Read from the contract rather than written by hand, so a role added, renamed
// or re-valued shows up here without anyone remembering to update a page. A
// hand-written foundations page is the first thing to drift, and a drifted one
// is worse than none: it is what a design reviewer reads.

// CSF meta. Story files are discovered by glob, but each one still has to
// declare a default export: the indexer reads it before the preview builds, so
// a file without one is not skipped — it fails the whole build.
export default { title: "Shared UI/Foundations" };

export const TypeScale = {
	render: () => (
		<div className="flex flex-col gap-3">
			{FRONTEND_TYPE_SCALE_STEPS.map((step) => (
				<div key={step} className="flex items-baseline gap-4">
					<code className="type-caption-plain w-20 shrink-0 text-muted-foreground">
						step {step}
					</code>
					<code className="type-caption-plain w-24 shrink-0 text-muted-foreground">
						{DEFAULT_TYPE_SCALE[step]}
					</code>
					<span style={{ fontSize: DEFAULT_TYPE_SCALE[step] }}>
						The quick brown fox
					</span>
				</div>
			))}
		</div>
	),
};

export const TypeRoles = {
	render: () => (
		<div className="flex flex-col gap-4">
			{FRONTEND_TYPE_ROLE_NAMES.map((name) => {
				const role = DEFAULT_TYPE_ROLES[name];
				const resolved = resolveTypeSlot(
					{
						...DEFAULT_FRONTEND_APPEARANCE,
						typeSlots: { ...DEFAULT_TYPE_SLOTS },
					},
					// Every role is reachable through at least one slot; find one so the
					// sample renders through the same path a component uses.
					(FRONTEND_TYPE_SLOT_NAMES.find(
						(slot) => DEFAULT_TYPE_SLOTS[slot] === name,
					) ?? "body") as (typeof FRONTEND_TYPE_SLOT_NAMES)[number],
				);
				return (
					<div key={name} className="flex flex-col gap-1">
						<code className="type-caption-plain text-muted-foreground">
							{name} · size {role.size ?? "inherit"} · weight{" "}
							{role.weight ?? "inherit"} · leading{" "}
							{role.lineHeight ?? "inherit"}
						</code>
						<span
							style={{
								fontSize: resolved.fontSize,
								fontWeight: resolved.fontWeight,
								lineHeight: resolved.lineHeight,
								letterSpacing: resolved.letterSpacing,
							}}
						>
							The quick brown fox jumps over the lazy dog
						</span>
					</div>
				);
			})}
		</div>
	),
};

export const ControlRungs = {
	render: () => (
		<div className="flex flex-col gap-4">
			{FRONTEND_CONTROL_SIZE_NAMES.map((size) => (
				<div key={size} className="flex items-center gap-4">
					<code className="type-caption-plain w-40 shrink-0 text-muted-foreground">
						{size} · {DEFAULT_CONTROL_SIZES[size].height} units ·{" "}
						{DEFAULT_CONTROL_SIZES[size].text}
					</code>
					<Button size={size}>Save changes</Button>
					<SegmentedControl
						size={size}
						options={[
							{ value: "list", label: "List" },
							{ value: "grid", label: "Grid" },
						]}
						value="list"
						onValueChange={() => {}}
					/>
				</div>
			))}
		</div>
	),
};

function SegmentedControlDemo() {
	const [view, setView] = useState("list");
	return (
		<SegmentedControl
			options={[
				{ value: "list", label: "List" },
				{ value: "grid", label: "Grid" },
				{ value: "map", label: "Map", disabled: true },
			]}
			value={view}
			onValueChange={setView}
		/>
	);
}

export const SegmentedControlStory = { render: () => <SegmentedControlDemo /> };

function PaginationDemo({ pageCount }: { pageCount: number }) {
	const [page, setPage] = useState(1);
	return (
		<Pagination page={page} pageCount={pageCount} onPageChange={setPage} />
	);
}

export const PaginationShort = {
	render: () => <PaginationDemo pageCount={7} />,
};
export const PaginationLong = {
	render: () => <PaginationDemo pageCount={40} />,
};

export const Fields = {
	render: () => (
		<div className="flex max-w-sm flex-col gap-4">
			<Field label="Workspace name">
				{(control) => <Input {...control} placeholder="Acme" />}
			</Field>
			<Field label="Contact" description="We never share it" required>
				{(control) => <Input {...control} placeholder="user@example.com" />}
			</Field>
			<Field label="Billing email" error="That address is not valid">
				{(control) => <Input {...control} defaultValue="not-an-address" />}
			</Field>
		</div>
	),
};

export const TableWithToolbar = {
	render: () => (
		<div>
			<TableToolbar>
				<span>3 members</span>
				<Button size="sm" variant="outline">
					Invite
				</Button>
			</TableToolbar>
			<Table>
				<TableHeader>
					<TableRow>
						<TableHead>Name</TableHead>
						<TableHead>Role</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					<TableRow>
						<TableCell>Jane Doe</TableCell>
						<TableCell>Administrator</TableCell>
					</TableRow>
				</TableBody>
			</Table>
		</div>
	),
};

export const TableEmpty = {
	render: () => (
		<Table>
			<TableHeader>
				<TableRow>
					<TableHead>Name</TableHead>
					<TableHead>Role</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				<TableEmptyState colSpan={2}>No members yet</TableEmptyState>
			</TableBody>
		</Table>
	),
};
