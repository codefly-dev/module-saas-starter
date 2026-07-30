import type { ServiceEndpoint } from "codefly";

export interface AccountsBindings {
  readonly rest?: string;
  readonly connect?: string;
}

export interface AccountsBindingOptions {
  readonly endpoints?: readonly ServiceEndpoint[];
  readonly currentModule?: string;
  readonly environment?: Readonly<Record<string, string | undefined>>;
}

/**
 * Resolves auth-sidecar/rest for complete module runs. Direct Accounts
 * endpoints are accepted only for isolated frontend tests.
 */
export function resolveAccountsBindings(options?: AccountsBindingOptions): AccountsBindings;
export function requireAccountsConnect(options?: AccountsBindingOptions): string;
