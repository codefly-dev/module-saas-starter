import { Code, ConnectError } from "@connectrpc/connect";

/**
 * The document the next revision starts from: the current ceiling with its
 * revision advanced, or an empty ceiling when none has been written. Nothing is
 * added for the administrator — which principals to allow, and how far, is the
 * decision this page exists for.
 */
export function nextRevisionDraft(
	document: string | undefined,
	revision: bigint,
): string {
	const next = Number(revision) + 1;
	if (document) {
		try {
			const parsed: unknown = JSON.parse(document);
			if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
				return JSON.stringify({ ...parsed, revision: next }, null, 2);
			}
		} catch {
			// A stored document the browser cannot read is shown as written.
		}
		return document;
	}
	return JSON.stringify({ revision: next, principals: {} }, null, 2);
}

/** Why a write was refused, in words the administrator can act on. */
export function writeRefusal(error: unknown): string {
	const refused = ConnectError.from(error);
	switch (refused.code) {
		case Code.FailedPrecondition:
			if (refused.rawMessage === "mfa_required") {
				return "Writing the envelope needs your passkey confirmed within the last 15 minutes. Confirm it (the sign-in page brings you back here), then write it again.";
			}
			return refused.rawMessage;
		case Code.Aborted:
			return "Another administrator wrote a revision after you started this draft. Nothing was written: discard the draft, review their revision, and decide again.";
		case Code.PermissionDenied:
			return "Only a platform super administrator can write the module envelope.";
		default:
			return refused.rawMessage || refused.message;
	}
}
