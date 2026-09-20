// The one place that touches `window.localStorage` / `window.sessionStorage`.
//
// Reading either property is itself a call that can throw: a sandboxed iframe,
// blocked third-party site data, or a hardened privacy setting raises a
// SecurityError from the getter, before any `getItem`. A store that receives
// `null` degrades toward the safe direction on its own; this makes sure the
// getter never gets the chance to throw at module evaluation or inside a render,
// where nothing catches it.
//
// Framework-free, like every other module under `application/`.

export type BrowserStorageKind = "local" | "session";

export function browserStorage(
	kind: BrowserStorageKind,
	scope: Pick<
		typeof globalThis,
		"localStorage" | "sessionStorage"
	> | null = typeof window === "undefined" ? null : window,
): Storage | null {
	if (scope === null) return null;
	try {
		const storage =
			kind === "local" ? scope.localStorage : scope.sessionStorage;
		return storage ?? null;
	} catch {
		return null;
	}
}
