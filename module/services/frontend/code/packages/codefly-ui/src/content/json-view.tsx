"use client";

// A JSON tree that stays responsive whatever it is handed. Three bounds keep a
// large payload from freezing the page:
//
//   - depth: nodes deeper than `expandDepth` start collapsed, and a collapsed
//     node renders none of its children;
//   - breadth: an open object or array renders `pageSize` children, then a
//     "Show more" control for the next page;
//   - length: a long string shows its first `STRING_PREVIEW` characters until
//     expanded.
//
// So the DOM a payload produces is bounded by what the reader opened, not by
// the payload's size. Every toggle is a native button (Tab, Enter, Space) with
// `aria-expanded`, and the copy button copies the whole document.

import { ChevronRight } from "lucide-react";
import { type ReactNode, useState } from "react";
import { cn } from "../layout/cn.js";
import { CopyButton } from "./copy-button.js";
import { readJson } from "./detect.js";
import { TONE } from "./tones.js";
import { TextBlock } from "./text-block.js";

export interface JsonViewProps {
	/** Structured data, or a JSON string (parsed; unparseable text renders as text). */
	value: unknown;
	/** Levels open on first render: 0 = all collapsed, 1 = the root open. Default 1. */
	expandDepth?: number;
	/** Children rendered per page of an open object or array. Default 100. */
	pageSize?: number;
	/** Show a copy button for the whole document. Default true. */
	copyable?: boolean;
	/** Accessible name for the tree. Default "JSON". */
	label?: string;
	className?: string;
}

const STRING_PREVIEW = 300;

type Container = Record<string, unknown> | unknown[];

function isContainer(value: unknown): value is Container {
	return (
		typeof value === "object" && value !== null && !(value instanceof Date)
	);
}

/** Pretty JSON for the clipboard; a cycle or a BigInt never throws. */
export function stringifyJson(value: unknown): string {
	const seen = new WeakSet<object>();
	try {
		return (
			JSON.stringify(
				value,
				(_key, current: unknown) => {
					if (typeof current === "bigint") return current.toString();
					if (typeof current === "object" && current !== null) {
						if (seen.has(current)) return "[Circular]";
						seen.add(current);
					}
					return current;
				},
				2,
			) ?? String(value)
		);
	} catch {
		return String(value);
	}
}

function entriesOf(value: Container): Array<[string, unknown]> {
	return Array.isArray(value)
		? value.map((item, index) => [String(index), item])
		: Object.entries(value);
}

function Key({ name, isIndex }: { name: string; isIndex: boolean }) {
	return (
		<>
			<span className={isIndex ? TONE.muted : TONE.title}>
				{isIndex ? name : JSON.stringify(name)}
			</span>
			<span className="text-muted-foreground">: </span>
		</>
	);
}

function StringLeaf({ value }: { value: string }) {
	const [full, setFull] = useState(false);
	const long = value.length > STRING_PREVIEW;
	const shown = long && !full ? value.slice(0, STRING_PREVIEW) : value;
	return (
		<>
			<span
				className={cn(
					TONE.string,
					"whitespace-pre-wrap [overflow-wrap:anywhere]",
				)}
			>
				{JSON.stringify(shown).slice(0, long && !full ? -1 : undefined)}
				{long && !full ? '…"' : null}
			</span>
			{long && (
				<button
					type="button"
					onClick={() => setFull((current) => !current)}
					className="ml-2 rounded text-primary underline underline-offset-2 outline-none focus-visible:ring-2 focus-visible:ring-ring"
				>
					{full
						? "Show less"
						: `Show all ${value.length.toLocaleString()} characters`}
				</button>
			)}
		</>
	);
}

function Leaf({ value }: { value: unknown }) {
	if (typeof value === "string") return <StringLeaf value={value} />;
	if (typeof value === "number")
		return <span className={TONE.number}>{String(value)}</span>;
	if (typeof value === "bigint")
		return <span className={TONE.number}>{`${value}n`}</span>;
	if (typeof value === "boolean")
		return <span className={TONE.keyword}>{String(value)}</span>;
	if (value === null) return <span className={TONE.keyword}>null</span>;
	if (value instanceof Date)
		return <span className={TONE.string}>{JSON.stringify(value)}</span>;
	if (value === undefined)
		return <span className="text-muted-foreground">undefined</span>;
	return <span className="text-muted-foreground">{`[${typeof value}]`}</span>;
}

interface NodeProps {
	name?: string;
	isIndex?: boolean;
	value: unknown;
	depth: number;
	expandDepth: number;
	pageSize: number;
	ancestors: ReadonlySet<object>;
	last: boolean;
}

function Comma({ last }: { last: boolean }) {
	return last ? null : <span className="text-muted-foreground">,</span>;
}

function JsonNode(props: NodeProps) {
	const { name, isIndex = false, value, last, ancestors } = props;
	const label =
		name === undefined ? null : <Key name={name} isIndex={isIndex} />;
	if (!isContainer(value)) {
		return (
			<div className="pl-5">
				{label}
				<Leaf value={value} />
				<Comma last={last} />
			</div>
		);
	}
	if (ancestors.has(value)) {
		return (
			<div className="pl-5">
				{label}
				<span className="text-muted-foreground">[Circular]</span>
				<Comma last={last} />
			</div>
		);
	}
	return <ContainerNode {...props} value={value} label={label} />;
}

function ContainerNode({
	value,
	label,
	depth,
	expandDepth,
	pageSize,
	ancestors,
	last,
}: NodeProps & { value: Container; label: ReactNode }) {
	const [open, setOpen] = useState(depth < expandDepth);
	const [visible, setVisible] = useState(pageSize);
	const isArray = Array.isArray(value);
	const [openBrace, closeBrace] = isArray ? ["[", "]"] : ["{", "}"];
	// Counting keys is O(n) but touches no DOM; the entries themselves are only
	// materialised while the node is open.
	const size = isArray ? value.length : Object.keys(value).length;
	const summary = isArray
		? `${size} ${size === 1 ? "item" : "items"}`
		: `${size} ${size === 1 ? "key" : "keys"}`;

	if (size === 0) {
		return (
			<div className="pl-5">
				{label}
				<span className="text-muted-foreground">{`${openBrace}${closeBrace}`}</span>
				<Comma last={last} />
			</div>
		);
	}

	const entries = open ? entriesOf(value) : [];
	const shown = entries.slice(0, visible);
	const hidden = entries.length - shown.length;
	const inner = new Set(ancestors).add(value);

	return (
		<div>
			<button
				type="button"
				aria-expanded={open}
				onClick={() => setOpen((current) => !current)}
				className="flex w-full items-start gap-1 rounded text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
			>
				<ChevronRight
					aria-hidden="true"
					className={cn(
						"mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform",
						open && "rotate-90",
					)}
				/>
				<span className="min-w-0 [overflow-wrap:anywhere]">
					{label}
					<span className="text-muted-foreground">{openBrace}</span>
					{!open && (
						<>
							<span className="px-1 text-muted-foreground italic">
								{summary}
							</span>
							<span className="text-muted-foreground">{closeBrace}</span>
							<Comma last={last} />
						</>
					)}
				</span>
			</button>
			{open && (
				<>
					<div className="ml-2 border-l border-border pl-2">
						{shown.map(([key, child], index) => (
							<JsonNode
								key={key}
								name={key}
								isIndex={isArray}
								value={child}
								depth={depth + 1}
								expandDepth={expandDepth}
								pageSize={pageSize}
								ancestors={inner}
								last={index === entries.length - 1}
							/>
						))}
						{hidden > 0 && (
							<button
								type="button"
								onClick={() => setVisible((current) => current + pageSize)}
								className="ml-5 rounded text-primary underline underline-offset-2 outline-none focus-visible:ring-2 focus-visible:ring-ring"
							>
								{`Show ${Math.min(pageSize, hidden).toLocaleString()} more (${hidden.toLocaleString()} hidden)`}
							</button>
						)}
					</div>
					<div className="pl-5">
						<span className="text-muted-foreground">{closeBrace}</span>
						<Comma last={last} />
					</div>
				</>
			)}
		</div>
	);
}

const NO_ANCESTORS: ReadonlySet<object> = new Set();

/**
 * A collapsible, copyable JSON tree. Accepts structured data or a JSON string;
 * a string that does not parse is shown verbatim as text rather than dropped.
 */
export function JsonView({
	value,
	expandDepth = 1,
	pageSize = 100,
	copyable = true,
	label = "JSON",
	className,
}: JsonViewProps) {
	const reading =
		typeof value === "string"
			? readJson(value)
			: ({ ok: true, value } as const);
	if (!reading.ok)
		return <TextBlock text={String(value)} className={className} />;
	const data = reading.value;
	return (
		<figure
			aria-label={label}
			data-slot="content-json"
			className={cn(
				"group/json relative rounded-md border border-border bg-muted/40 font-mono type-caption-plain text-foreground",
				className,
			)}
		>
			{copyable && (
				<div className="absolute top-1 right-1 z-10">
					<CopyButton text={() => stringifyJson(data)} label={label} />
				</div>
			)}
			<div className={cn("overflow-auto p-3", copyable && "pr-10")}>
				{isContainer(data) ? (
					<JsonNode
						value={data}
						depth={0}
						expandDepth={Math.max(0, expandDepth)}
						pageSize={Math.max(1, pageSize)}
						ancestors={NO_ANCESTORS}
						last
					/>
				) : (
					<Leaf value={data} />
				)}
			</div>
		</figure>
	);
}
