import { Code, ConnectError } from "@connectrpc/connect";

// The codes under which the host answers in words meant for a person: it
// refused the act, and says why. Every other failure is the transport's, a
// timeout's or a defect's, whose text is for an engineer.
const SPOKEN: ReadonlySet<Code> = new Set([
	Code.PermissionDenied,
	Code.FailedPrecondition,
	Code.InvalidArgument,
	Code.AlreadyExists,
	Code.NotFound,
	Code.ResourceExhausted,
]);

/**
 * What to tell a person when an act did not happen. `doing` is the act as they
 * would say it ("save the team", "revoke the key"). A refusal the host worded
 * is passed on as it worded it; anything else is said plainly, without the
 * error's own text, which names transports and internals.
 */
export function failureText(doing: string, error: unknown): string {
	if (error instanceof ConnectError && SPOKEN.has(error.code)) {
		const said = error.rawMessage.trim();
		if (said) return `Couldn't ${doing}: ${said}`;
	}
	return `Couldn't ${doing}. Try again; if it keeps failing, tell your administrator.`;
}
