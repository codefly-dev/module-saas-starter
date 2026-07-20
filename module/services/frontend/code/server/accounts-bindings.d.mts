import type { ServiceEndpoint } from "codefly";

export interface AccountsBindings {
  readonly rest?: string;
  readonly connect?: string;
}

export interface AccountsBindingOptions {
  readonly endpoints?: readonly ServiceEndpoint[];
  readonly currentModule?: string;
}

export function resolveAccountsBindings(options?: AccountsBindingOptions): AccountsBindings;
export function requireAccountsConnect(options?: AccountsBindingOptions): string;
