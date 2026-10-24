import { Code, ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";

/**
 * The host refuses a sensitive operation — a role grant, an impersonation, a
 * platform role, a module envelope — with `mfa_required` when the caller's
 * second factor was confirmed more than fifteen minutes ago (SP-IDENT-10). A
 * refresh never renews that evidence; only signing in again does. This is the
 * one place the frontend turns that refusal into something a person can act on:
 * confirm the passkey, come back to the same page, retry.
 */
export const STEP_UP_MESSAGE =
	"This change needs your passkey confirmed within the last 15 minutes.";

/** Whether an error is the host's `mfa_required` refusal, however it travelled. */
export function isMFARequired(error: unknown): boolean {
	if (error instanceof ConnectError) {
		return (
			error.code === Code.FailedPrecondition &&
			error.rawMessage === "mfa_required"
		);
	}
	const message = error instanceof Error ? error.message : String(error ?? "");
	return (
		message.includes("mfa_required") &&
		(message.includes("failed_precondition") ||
			message.includes("FailedPrecondition"))
	);
}

/**
 * Where to send the person to confirm the passkey and come back: the sign-in
 * page already honours `next` as its post-login destination, and `step_up`
 * tells it to say why rather than "Welcome back".
 */
export function stepUpHref(returnTo: string): string {
	return `/auth/login?step_up=1&next=${encodeURIComponent(returnTo)}`;
}

function here(): string {
	if (typeof window === "undefined") return "/";
	return `${window.location.pathname}${window.location.search}`;
}

/** A refusal toast whose action takes the person to confirm the passkey. */
export function stepUpToast(returnTo: string = here()): void {
	toast.error(STEP_UP_MESSAGE, {
		action: {
			label: "Confirm passkey",
			onClick: () => {
				window.location.assign(stepUpHref(returnTo));
			},
		},
	});
}

/**
 * The codes under which the host's message is a sentence written for whoever
 * reads it: the rule the request broke, the field that was wrong, the record
 * that already exists, the limit that was hit. The host guarantees it — a
 * fault travels as Internal with a fixed sentence, never its call path — so
 * these are shown as sent. Every other code gets the screen's own fallback.
 */
const PERSON_FACING_CODES: ReadonlySet<Code> = new Set([
	Code.InvalidArgument,
	Code.NotFound,
	Code.AlreadyExists,
	Code.PermissionDenied,
	Code.FailedPrecondition,
	Code.ResourceExhausted,
	Code.Unauthenticated,
]);

/**
 * The message for a refused mutation: the host's own sentence when it wrote
 * one for the person ("this address already has a pending invitation…",
 * "seats quota exhausted: used 5 of 5"), the step-up sentence for
 * `mfa_required`, and the screen's fallback otherwise.
 */
export function refusalMessage(error: unknown, fallback: string): string {
	if (isMFARequired(error)) return STEP_UP_MESSAGE;
	if (!(error instanceof ConnectError)) return fallback;
	if (!PERSON_FACING_CODES.has(error.code)) return fallback;
	return error.rawMessage || fallback;
}

/**
 * The toast for a refused mutation: the step-up when the host asked for a
 * recent factor, the host's reason or the screen's own message otherwise.
 */
export function refusalToast(error: unknown, fallback: string): void {
	if (isMFARequired(error)) {
		stepUpToast();
		return;
	}
	toast.error(refusalMessage(error, fallback));
}
