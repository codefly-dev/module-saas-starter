import { ConnectError } from "@connectrpc/connect";

/**
 * A failed call in words a person can read: the host's own message, with the
 * gRPC envelope (`rpc error: code = … desc = `) stripped.
 *
 * One definition, because every datasource surface renders the same failures —
 * a refused credential, an unreachable host — and a second copy drifts into
 * showing the raw envelope to half of them.
 */
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

export function messageOf(error: unknown): string {
	const message =
		error instanceof ConnectError
			? error.rawMessage
			: error instanceof Error
				? error.message
				: "unexpected error";
	return message.replace(/^rpc error: code = \w+ desc = /, "");
}
