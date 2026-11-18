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
				return "Confirm your passkey before saving this configuration. After signing in, return here to review and save your draft.";
			}
			return "The current configuration cannot be applied. Refresh the page and review the latest revision.";
		case Code.Aborted:
			return "Another administrator wrote a revision after you started this draft. Nothing was written: discard the draft, review their revision, and decide again.";
		case Code.PermissionDenied:
			return "You don’t have access to change this configuration. Ask a platform administrator for access.";
		case Code.InvalidArgument:
			return "The configuration is invalid. Check the document before saving it.";
		case Code.Unauthenticated:
			return "Sign in again before saving this configuration.";
		default:
			return "Couldn’t save this configuration. Try again; if this keeps happening, contact your platform administrator.";
	}
}
