import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { bindingBearer, type SolutionRequestBinding } from "./binding.js";
import { injectTraceContext } from "./tracing.js";

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
 *
 * A request bound for this page's own origin that does not already carry a
 * `traceparent` gets one from the active trace context, so a span the caller
 * has open becomes the backend's parent. A caller that tracks its own context
 * across awaits passes the header per call instead, and it is left as is.
 * Without `installBrowserTracing` there is no active span and nothing is added.
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
				const bearer = bindingBearer(binding);
				if (bearer) request.header.set("authorization", `Bearer ${bearer}`);
				injectTraceContext(request.header, request.url);
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
