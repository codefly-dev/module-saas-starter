import { Code, ConnectError } from "@connectrpc/connect";

// The organization lifecycle refusals the server words for the person in front
// of the screen: the last administrator, the sole member, a taken slug, a
// mistyped confirmation, a creation policy. Each is a rule they can act on, so
// the server's sentence is shown as it is. Any other failure falls back to a
// generic message — an internal error's text is a call path, not something to
// render to a user.
const ACTIONABLE = new Set([
	Code.FailedPrecondition,
	Code.AlreadyExists,
	Code.PermissionDenied,
	Code.InvalidArgument,
	Code.NotFound,
]);

export function lifecycleErrorMessage(
	error: unknown,
	fallback: string,
): string {
	if (!(error instanceof ConnectError)) return fallback;
	if (!ACTIONABLE.has(error.code)) return fallback;
	return error.rawMessage || fallback;
}
