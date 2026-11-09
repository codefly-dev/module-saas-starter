"use client";

import { useRegisteredSolutions } from "@/solutions/SolutionsMenu";

/**
 * The titles this person's own menu gives each target, by route alias
 * ("Expense Reports" for expenses). Admin listed targets by alias while
 * the sidebar and the home page named them; the menu's projection is the one
 * place a target's title is read, so Admin reads it there too. A target the
 * person has no menu entry for keeps its alias.
 */
export function useTargetTitles(): ReadonlyMap<string, string> {
	const solutions = useRegisteredSolutions();
	return new Map(
		solutions.map((solution) => [solution.id, solution.nav.title]),
	);
}

/** A target's title, with its alias beside it when the two differ. */
export function TargetName({
	alias,
	titles,
}: {
	alias: string;
	titles: ReadonlyMap<string, string>;
}) {
	const title = titles.get(alias);
	if (!title || title === alias) return <>{alias}</>;
	return (
		<span className="inline-flex flex-col">
			<span>{title}</span>
			<span className="font-mono text-xs font-normal text-muted-foreground">
				{alias}
			</span>
		</span>
	);
}
