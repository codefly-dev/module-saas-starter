// The colours the content tier paints syntax with — keys, strings, numbers,
// keywords — as token-derived utilities only.
//
// The skin's chart palette is its set of distinct hues, so syntax takes its hue
// from there. But a chart colour is chosen to read as a FILL, not as text: the
// default palette's lightest step all but vanishes on a light surface, and its
// darkest on a dark one. So each tone is the chart colour mixed toward the
// foreground, which keeps a skin's hue while guaranteeing the text sits on the
// readable side of the surface it is drawn on, in light and dark alike.
//
// Tailwind reads these class names from this file, so they must stay literal.

export const TONE = {
	/** Keywords, booleans, null. */
	keyword: "text-[color-mix(in_oklab,var(--chart-1)_55%,var(--foreground))]",
	/** Strings. */
	string: "text-[color-mix(in_oklab,var(--chart-2)_55%,var(--foreground))]",
	/** Numbers and literals. */
	number: "text-[color-mix(in_oklab,var(--chart-3)_55%,var(--foreground))]",
	/** Titles, function and type names, object keys. */
	title: "text-[color-mix(in_oklab,var(--chart-4)_55%,var(--foreground))]",
	/** Attributes, properties, variables, built-ins. */
	attribute: "text-[color-mix(in_oklab,var(--chart-5)_55%,var(--foreground))]",
	/** Comments and punctuation. */
	muted: "text-muted-foreground",
	/** Removed lines in a diff. */
	deletion: "text-destructive",
} as const;
