// A tiny classnames joiner, inlined so the dashboard kit carries no dependency
// (the app's `@/lib/utils` `cn` pulls clsx+tailwind-merge; the kit does not need
// conflict-resolution, only truthy joining).
export function cn(...parts: Array<string | false | null | undefined>): string {
	return parts.filter(Boolean).join(" ");
}
