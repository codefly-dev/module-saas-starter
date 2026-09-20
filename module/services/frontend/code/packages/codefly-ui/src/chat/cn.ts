// The chat tier's class joiner. Uses clsx + tailwind-merge so a caller's
// `className` (passed last) wins conflicting Tailwind utilities against a
// component's own classes — the same override semantics as the layout tier's
// `cn`. Kept as a self-contained per-subpath file so `@codefly-dev/ui/chat`
// bundles independently, but behaviourally identical to `layout/cn` and
// `dashboard/cn`.
import { type ClassValue, clsx } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// The skin's type slots (`type-card-title`) and control rungs (`control-sm`) are
// custom utilities, so stock tailwind-merge does not know them. What it must
// know is narrow, and deliberately so.
//
// A slot or rung is COMPOSITE: one class sets several properties. tailwind-merge
// can only keep or drop a whole class, so declaring `text-lg` as conflicting with
// `type-card-title` would make a caller's size override delete the slot's
// weight, line height and family with it — and `h-11` would strip a button's
// padding and text along with its height. Instead the generated utilities sit at
// zero specificity (`:where(&)`, see scripts/generate-type-utilities.mjs in
// this package), so a core utility on the same element overrides exactly the property it
// names, whatever the source order. `cn` keeps both classes and the cascade does
// the partial override no merge could.
//
// What `cn` does resolve is skin-against-skin, where two whole shapes really do
// replace each other and the later one must win:
//
//   type-<slot>            the slot's text: two slots on one element is a bug
//   control-<size>         height, inline padding and the rung's text
//   control-height-<size>  height alone (a container sized to a rung)
//   control-icon-<size>    a square control: height, width, no inline padding
//   control-glyph-<size>   the glyph inside a control: width and height
//
// A slot beside a rung is left alone on purpose: the slot's text is emitted
// after the rung's and wins by source order, which is the intended reading.
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
			// A later full rung replaces an earlier one of any shape that also
			// sets the height. A later height-only rung does NOT replace a full
			// rung: it is emitted after it and overrides the height alone.
			"skin-control": ["skin-control-height", "skin-control-icon"],
			"skin-control-icon": ["skin-control", "skin-control-height"],
		},
	},
});

export function cn(...inputs: ClassValue[]): string {
	return twMerge(clsx(inputs));
}
