import type { ServiceEndpoint } from "codefly";

export interface AccountsBindings {
	readonly rest: string;
	readonly connect: string;
}

export interface AccountsBindingOptions {
	readonly endpoints?: readonly ServiceEndpoint[];
	readonly currentModule?: string;
	readonly environment?: Readonly<Record<string, string | undefined>>;
}

/**
 * Resolves auth-gateway/rest, the frontend's only product API path, and throws
 * when the composition declares none. Read at runtime by every caller; no
 * build ever freezes the result.
 */
export function resolveAccountsBindings(
	options?: AccountsBindingOptions,
): AccountsBindings;
export function requireAccountsConnect(
	options?: AccountsBindingOptions,
): string;
