"use client";

import { type Timestamp, timestampDate } from "@bufbuild/protobuf/wkt";

// What a source's declared trigger says, as the Unattended work playground
// shows it. The Data sources page says a schedule in one sentence per source;
// this is the operator's reading of the declaration behind it.

/** One labelled fact of a declaration. */
export function Fact({
	label,
	value,
	mono,
}: {
	label: string;
	value: string;
	mono?: boolean;
}) {
	return (
		<div>
			<dt className="text-xs text-muted-foreground">{label}</dt>
			<dd className={mono ? "font-mono text-xs break-all" : "text-sm"}>
				{value}
			</dd>
		</div>
	);
}

/**
 * Which arm the declaration names, in words. A declaration with neither is the
 * state an installing module refuses rather than running the work as itself,
 * so it is said plainly instead of being left blank.
 *
 * The reference arm does NOT name which module's delegation it is (issue
 * #1112, m5). The reference value is the source id, so every delegation on one
 * source reads back the same and the host resolves whichever module holds an
 * active one at mint. Naming one here would be this page's own guess: an
 * operator whose executing module holds no delegation would read that a
 * reference is bound to it when another module's is. The references table
 * names each module, which is where that question is answerable.
 */
export function declaredIdentity(declaration?: {
	identity?: { source: string; binding: string };
}): string {
	const identity = declaration?.identity;
	if (identity?.source)
		return "A delegation reference — whichever module holds an active one";
	if (identity?.binding) return "An installation binding";
	return "None — occurrences would be refused";
}

/**
 * One instant, or an em dash. Absent is a real answer here — a source that has
 * never reconciled has no last occurrence — so it is rendered rather than
 * defaulted to a time nothing happened at.
 */
export function instant(value?: Timestamp): string {
	if (!value) return "—";
	return timestampDate(value).toLocaleString();
}
