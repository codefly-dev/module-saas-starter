import { ConnectError } from "@connectrpc/connect";

/**
 * A failed call in words a person can read: the host's own message, with the
 * gRPC envelope (`rpc error: code = … desc = `) stripped.
 *
 * One definition, because every datasource surface renders the same failures —
 * a refused credential, an unreachable host — and a second copy drifts into
 * showing the raw envelope to half of them.
 */
export function messageOf(error: unknown): string {
	const message =
		error instanceof ConnectError
			? error.rawMessage
			: error instanceof Error
				? error.message
				: "unexpected error";
	return message.replace(/^rpc error: code = \w+ desc = /, "");
}
