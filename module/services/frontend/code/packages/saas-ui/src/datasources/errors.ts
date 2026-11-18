import { Code, ConnectError } from "@connectrpc/connect";

/**
 * The key-filter field a host refusal names, read from the message's leading
 * path (`upload.include_patterns[1]: …`), which the host states as part of the
 * contract so a client without the error-detail types can place it.
 */
export function refusedKeyFilterField(
	message: string | undefined,
): "includePatterns" | "excludePatterns" | "fileExtensions" | undefined {
	const field =
		/^(?:upload\.)?(include_patterns|exclude_patterns|file_extensions)(?:\[\d+\])?: /.exec(
			message ?? "",
		)?.[1];
	return field === "include_patterns"
		? "includePatterns"
		: field === "exclude_patterns"
			? "excludePatterns"
			: field === "file_extensions"
				? "fileExtensions"
				: undefined;
}

/** Internal matching only. Never render this diagnostic text. */
export function failureReason(error: unknown): string {
	const message =
		error instanceof ConnectError
			? error.rawMessage
			: error instanceof Error
				? error.message
				: "unexpected error";
	return message.replace(/^rpc error: code = \w+ desc = /, "");
}

/** Safe presentation shared by every source management surface. */
export function messageOf(error: unknown): string {
	const reason = failureReason(error);
	if (/GitHub rejected the access token/i.test(reason))
		return "GitHub could not read this repository. Replace its saved token with one that has access.";
	if (
		/GitHub App setup link has already been used or has expired/i.test(reason)
	)
		return "This setup link has expired or was already used. Start the GitHub connection again.";
	if (
		/GitHub App installation.*not connected to this organization/i.test(reason)
	)
		return "Connect the GitHub app to this organization before switching this source.";
	if (/changed after this form was opened/i.test(reason))
		return "This source changed while you were editing. Reopen the form to review the latest settings.";

	if (error instanceof ConnectError) {
		switch (error.code) {
			case Code.PermissionDenied:
				return "You don’t have access. Ask an organization administrator to grant it.";
			case Code.Unauthenticated:
				return "Sign in again to continue.";
			case Code.InvalidArgument:
				return "Check the connection details and try again.";
			case Code.FailedPrecondition:
				return "This action is not available yet. Check the connection setup before continuing.";
			case Code.NotFound:
				return "This source is no longer available. Refresh the page to see current sources.";
			case Code.Unavailable:
				return "The service is temporarily unavailable. Try again in a moment.";
		}
	}
	return "The request could not be completed. Check your connection and access; if it keeps failing, contact your administrator.";
}
