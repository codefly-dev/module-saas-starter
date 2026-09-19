// The dashboard tier's class joiner. Uses clsx + tailwind-merge so a caller's
// `className` (passed last) wins conflicting Tailwind utilities against a
// component's own classes — the same override semantics as the layout tier's
// `cn`. Kept as a self-contained per-subpath file so `@codefly-dev/ui/dashboard`
// bundles independently, but behaviourally identical to `layout/cn` and `chat/cn`.
import { type ClassValue, clsx } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// The skin's type slots (`type-card-title`) and control rungs (`control-sm`) are
// custom utilities, so stock tailwind-merge cannot know what they conflict with:
// it would keep BOTH `type-card-title` and a caller's `text-lg` and let CSS
// source order decide, which is exactly the silent override failure `cn` exists
// to prevent.
//
// Each group is declared by the properties it actually sets, not by its name.
// Lumping the four `control-*` shapes together would make a type slot delete a
// height-only rung standing beside it, which is how an input loses its height.
//
//   type-<slot>            font size, weight, line height, tracking, family
//   control-<size>         + height and inline padding
//   control-height-<size>  height alone (a container sized to a rung)
//   control-icon-<size>    a square control: height, width, no inline padding
//   control-glyph-<size>   the glyph inside a control: width and height
const TYPE = [
	"font-size",
	"font-weight",
	"leading",
	"tracking",
	"font-family",
] as const;
const SIZE_NAME = (value: string) =>
	["xs", "sm", "default", "lg"].includes(value);

const twMerge = extendTailwindMerge<
	| "skin-type"
	| "skin-control"
	| "skin-control-height"
	| "skin-control-icon"
	| "skin-control-glyph"
>({
	extend: {
		classGroups: {
			"skin-type": [{ type: [(value: string) => value.length > 0] }],
			"skin-control": [{ control: [SIZE_NAME] }],
			"skin-control-height": [{ control: [{ height: [SIZE_NAME] }] }],
			"skin-control-icon": [{ control: [{ icon: [SIZE_NAME] }] }],
			"skin-control-glyph": [{ control: [{ glyph: [SIZE_NAME] }] }],
		},
		conflictingClassGroups: {
			// A later raw utility wins over an earlier custom one...
			...Object.fromEntries(
				TYPE.map((group) => [
					group,
					["skin-type", "skin-control", "skin-control-icon"],
				]),
			),
			h: [
				"skin-control",
				"skin-control-height",
				"skin-control-icon",
				"skin-control-glyph",
			],
			w: ["skin-control-icon", "skin-control-glyph"],
			size: ["skin-control-icon", "skin-control-glyph"],
			px: ["skin-control", "skin-control-icon"],
			// ...and a later custom utility wins over what it replaces. A type slot
			// does NOT clear a height-only rung: they set disjoint properties.
			"skin-type": [...TYPE, "skin-control", "skin-control-icon"],
			"skin-control": [
				...TYPE,
				"h",
				"px",
				"skin-type",
				"skin-control-height",
				"skin-control-icon",
			],
			"skin-control-height": ["h", "skin-control", "skin-control-icon"],
			"skin-control-icon": [
				...TYPE,
				"h",
				"w",
				"size",
				"px",
				"skin-type",
				"skin-control",
				"skin-control-height",
			],
			"skin-control-glyph": ["h", "w", "size"],
		},
	},
});

export function cn(...inputs: ClassValue[]): string {
	return twMerge(clsx(inputs));
}
