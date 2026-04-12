// Pure domain types for auth. No React imports.

export interface AuthState {
  user: { id: string; email?: string } | null;
  accessToken: string | null;
  isLoading: boolean;
  isAuthenticated: boolean;
}

export interface ProviderPreset {
  id: string;
  displayName: string;
  authorizeURL: string;
  clientID: string;
  scope: string;
}
