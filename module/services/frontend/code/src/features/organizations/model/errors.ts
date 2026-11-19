import { Code, ConnectError } from "@connectrpc/connect";
import { failureText } from "@/shared/lib/failure-text";

/** Keep organization failures actionable without rendering server diagnostics. */
export function lifecycleErrorMessage(
	error: unknown,
	fallback: string,
): string {
	if (!(error instanceof ConnectError)) return fallback;
	if (error.code === Code.FailedPrecondition) {
		return "This change is not available in the organization's current state. Ask an organization administrator to review its members and settings.";
	}
	if (
		[
			Code.PermissionDenied,
			Code.Unauthenticated,
			Code.InvalidArgument,
			Code.AlreadyExists,
			Code.NotFound,
			Code.ResourceExhausted,
		].includes(error.code)
	) {
		return failureText("change this organization", error);
	}
	return fallback;
}
