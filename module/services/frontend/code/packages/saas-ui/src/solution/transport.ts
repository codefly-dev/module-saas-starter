import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { SolutionRequestBinding } from "./binding.js";

/**
 * A Connect transport over the host binding, for generated clients: the twin
 * of `solutionFetch`. At `apiBase` itself it reaches the host's platform
 * procedures (the host proxy routes `saas.*` there); at `apiBase` + `path` it
 * reaches whatever the solution's backend serves there — for a solution on
 * solution-runtime-go, `/modules/<as>` is a consumed module's passthrough.
 *
 * Every request carries the viewer's bearer and goes out through the host's
 * `authedFetch` (refresh, then one retry) when the host provides one. A
 * refused authority (unauthenticated or permission denied) is reported to
 * `onUnauthorized` and still rejects.
 */
export function solutionTransport(
	binding: SolutionRequestBinding,
	path = "",
	onUnauthorized?: () => void,
): Transport {
	return createConnectTransport({
		baseUrl: `${binding.apiBase}${path}`,
		fetch: (input, init) =>
			(binding.authedFetch ?? fetch)(input, {
				...init,
				credentials: "same-origin",
			}),
		interceptors: [
			(next) => async (request) => {
				const bearer = binding.getAccessToken?.();
				if (bearer) request.header.set("authorization", `Bearer ${bearer}`);
				try {
					return await next(request);
				} catch (failure) {
					if (
						failure instanceof ConnectError &&
						(failure.code === Code.Unauthenticated ||
							failure.code === Code.PermissionDenied)
					)
						onUnauthorized?.();
					throw failure;
				}
			},
		],
	});
}
