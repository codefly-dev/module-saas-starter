// The chat tier's class joiner. Uses clsx + tailwind-merge so a caller's
// `className` (passed last) wins conflicting Tailwind utilities against a
// component's own classes — the same override semantics as the layout tier's
// `cn`. Kept as a self-contained per-subpath file so `@codefly-dev/ui/chat`
// bundles independently, but behaviourally identical to `layout/cn` and
// `dashboard/cn`.
import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
	return twMerge(clsx(inputs));
}
