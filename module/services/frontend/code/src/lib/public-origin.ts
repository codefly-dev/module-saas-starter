/**
 * The public browser origin of a request, preferring the ingress-set forwarded
 * pair over the pod-local request URL — the same resolution src/proxy.ts uses,
 * because behind a TLS-terminating ingress the pod sees plaintext `http` and its
 * own host.
 */
export function requestPublicOrigin(request: Request): string {
	const url = new URL(request.url);
	const forwardedProto = request.headers
		.get("x-forwarded-proto")
		?.split(",")[0]
		?.trim();
	const forwardedHost = request.headers
		.get("x-forwarded-host")
		?.split(",")[0]
		?.trim();
	const protocol = forwardedProto ? `${forwardedProto}:` : url.protocol;
	const host = forwardedHost || url.host;
	return `${protocol}//${host}`;
}
