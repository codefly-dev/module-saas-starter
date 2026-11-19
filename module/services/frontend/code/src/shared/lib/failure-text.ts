import { Code, ConnectError } from "@connectrpc/connect";

/** Describe a failed action without exposing transport or server details. */
export function failureText(doing: string, error: unknown): string {
  if (error instanceof ConnectError) {
    switch (error.code) {
      case Code.PermissionDenied:
        return `You don't have access to ${doing}. Ask your organization administrator for access.`;
      case Code.Unauthenticated:
        return `Sign in again to ${doing}.`;
      case Code.InvalidArgument:
        return `Couldn't ${doing}. Check the information you entered.`;
      case Code.FailedPrecondition:
        return `Couldn't ${doing}. The current state doesn't allow this change. Refresh the page to review it.`;
      case Code.AlreadyExists:
        return `Couldn't ${doing}. An item with these details already exists.`;
      case Code.NotFound:
        return `Couldn't ${doing}. The item is no longer available. Refresh the page to review what's available.`;
      case Code.ResourceExhausted:
        return `Couldn't ${doing}. A usage limit has been reached. Ask your organization administrator to review it.`;
    }
  }
  return `Couldn't ${doing}. Try again; if it keeps failing, tell your administrator.`;
}
