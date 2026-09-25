import type { SolutionRequestBinding } from "./binding.js";

/**
 * A non-2xx answer from a solution's backend. `detail` is the backend's own
 * `{ "error": "…" }` message when it sent one — the solution runtime answers
 * every handler failure in that shape — so a remote can show the reason the
 * backend gave instead of a bare status.
 */
export class SolutionRequestError extends Error {
	readonly status: number;
	readonly detail: string | undefined;

	constructor(status: number, detail?: string) {
		super(detail ?? `HTTP ${status}`);
		this.name = "SolutionRequestError";
		this.status = status;
		this.detail = detail;
	}
}

/**
 * Sends one request to the solution's own backend, `path` relative to the
 * host-injected `apiBase`. It goes out same-origin through the host's
 * `authedFetch` (refresh-then-retry on a 401) when the host provides one, with
 * the current bearer stamped and JSON accepted. The response is returned as it
 * came; `solutionJson` is the checked, decoded form.
 */
export function solutionFetch(
	binding: SolutionRequestBinding,
	path: string,
	init: RequestInit = {},
): Promise<Response> {
	const bearer = binding.getAccessToken?.();
	const headers = new Headers(init.headers);
	if (!headers.has("accept")) headers.set("accept", "application/json");
	if (bearer) headers.set("authorization", `Bearer ${bearer}`);
	if (
		init.body !== undefined &&
		init.body !== null &&
		!headers.has("content-type")
	)
		headers.set("content-type", "application/json");
	return (binding.authedFetch ?? fetch)(`${binding.apiBase}${path}`, {
		credentials: "same-origin",
		...init,
		headers,
	});
}

/**
 * `solutionFetch`, then the JSON body — or a `SolutionRequestError` carrying
 * the status and the backend's own message for a non-2xx answer.
 */
export async function solutionJson<T>(
	binding: SolutionRequestBinding,
	path: string,
	init?: RequestInit,
): Promise<T> {
	const response = await solutionFetch(binding, path, init);
	if (!response.ok) {
		const body = (await response.json().catch(() => null)) as {
			error?: unknown;
		} | null;
		throw new SolutionRequestError(
			response.status,
			typeof body?.error === "string" ? body.error : undefined,
		);
	}
	return (await response.json()) as T;
}
