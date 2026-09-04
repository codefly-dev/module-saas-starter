// The layout tier's class joiner. Uses clsx + tailwind-merge so a caller's
// `className` (passed last) wins conflicting Tailwind utilities against a
// component's own classes and a `cva` variant set — the override semantics the
// promoted shadcn primitives (Button, Badge, …) depend on. Mirrors the host's
// `@/lib/utils` `cn`, so a primitive reads identically here and there.
import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
	return twMerge(clsx(inputs));
}
