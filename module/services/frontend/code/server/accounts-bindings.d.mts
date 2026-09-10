import type { ServiceEndpoint } from "codefly";

export interface AccountsBindings {
	readonly rest: string;
	readonly connect: string;
}

export interface IsolatedDirectAccounts {
	readonly rest: string;
	readonly connect: string;
}

export interface GatewayResolutionOptions {
	readonly endpoints?: readonly ServiceEndpoint[];
	readonly currentModule?: string;
	readonly environment?: Readonly<Record<string, string | undefined>>;
}

export interface AccountsBindingOptions extends GatewayResolutionOptions {
	/**
	 * Direct Accounts destinations for an in-process isolated test. Deliberately
	 * a call argument: no environment value can select it, and the running server
	 * passes no arguments at all.
	 */
	readonly isolatedDirectAccounts?: IsolatedDirectAccounts;
}

/**
 * Resolves auth-gateway/rest, the frontend's only product API path, and throws
 * when the composition declares none.
 */
export function resolveAccountsBindings(
	options?: AccountsBindingOptions,
): AccountsBindings;
/**
 * Build-time rewrite destinations; undefined when the build resolves no
 * gateway. Never names a direct Accounts destination.
 */
export function resolveProductAPIRewrites(
	options?: GatewayResolutionOptions,
): AccountsBindings | undefined;
export function requireAccountsConnect(
	options?: AccountsBindingOptions,
): string;
