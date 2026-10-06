"use client";

import { Button, Input, Label } from "@codefly-dev/ui/layout";
import type { ComponentProps, ReactNode } from "react";

/**
 * How a GitHub source authenticates, and the fields each mode needs.
 *
 * Lifted out of `connect-github-form.tsx` so a surface that fixes the
 * repository — `<DeclaredSourceCard>`, where the solution declares what it
 * reads and the person supplies only the credential — asks for the credential
 * in exactly the same words, with exactly the same three modes, without
 * copying the form. A second copy of this choice is the failure mode worth
 * naming: the App's "nothing to paste or rotate", the public path's 60
 * requests an hour, and the PAT's Contents: Read-only scope are the whole of
 * what a person has to decide between, and two surfaces that say them
 * differently teach two different products.
 *
 * Everything here is presentational and controlled. The form drives it through
 * react-hook-form; the card drives it from `useState`. Neither mode's meaning
 * lives here — the host decides what a missing token means (see
 * `AddGitHubSourceRequest.access_token`).
 */
export type CredentialMethod = "app" | "public" | "pat";

/** The shared error style for a field message. */
export const fieldErrorClass = "type-body text-destructive";

/**
 * Narrows a `<select>` value to a mode the caller can actually offer. The App
 * option is absent when the client cannot drive App onboarding, so a value of
 * `app` arriving from anywhere else falls back to the PAT path rather than
 * selecting a mode with no way to complete it.
 */
export function credentialMethodFrom(
	value: string,
	appAvailable: boolean,
): CredentialMethod {
	if (value === "app" && appAvailable) return "app";
	if (value === "public") return "public";
	return "pat";
}

const methodHelp: Record<CredentialMethod, string> = {
	app: "Install the app on repositories you choose. Nothing to create, paste, or rotate.",
	public:
		"No token needed, but GitHub allows only 60 unauthenticated requests an hour, so large repositories sync slowly — use the App or a token for those.",
	pat: "For an existing connection, or for development. Restrict the token to this repository with Contents: Read-only.",
};

/** The Authentication choice and the sentence explaining the selected mode. */
export function CredentialMethodField({
	id,
	value,
	appAvailable,
	onChange,
}: {
	id: string;
	/** Undefined reads as the PAT path, which is what an unset mode means. */
	value: CredentialMethod | undefined;
	appAvailable: boolean;
	onChange: (next: CredentialMethod) => void;
}) {
	const method = value ?? "pat";
	return (
		<div className="space-y-2">
			<Label htmlFor={id}>Authentication</Label>
			<select
				id={id}
				value={method}
				onChange={(event) =>
					onChange(credentialMethodFrom(event.target.value, appAvailable))
				}
			>
				{appAvailable && <option value="app">GitHub App (recommended)</option>}
				<option value="public">Public repository (no token)</option>
				<option value="pat">Fine-grained personal access token</option>
			</select>
			<p className="type-caption-plain text-muted-foreground">{methodHelp[method]}</p>
		</div>
	);
}

/**
 * The leg that sends the browser to GitHub to install the App.
 *
 * `description` is the caller's, because the two surfaces promise different
 * things on the way back: the form returns to a repository picker, the
 * declared card returns to a repository it already knows.
 */
export function AppInstallPrompt({
	description,
	onBeginAppSetup,
	phase,
	error,
}: {
	description: ReactNode;
	onBeginAppSetup: () => void;
	/** Which leg of App setup is in flight, if either. */
	phase?: "beginning" | "completing";
	error?: string;
}) {
	return (
		<div className="space-y-2">
			<p className="type-body text-muted-foreground">{description}</p>
			<Button
				type="button"
				variant="outline"
				onClick={onBeginAppSetup}
				disabled={!!phase}
				aria-busy={!!phase}
			>
				{phase === "completing"
					? "Completing setup…"
					: phase === "beginning"
						? "Opening GitHub…"
						: "Install or select repositories on GitHub"}
			</Button>
			{error && (
				<p role="alert" className={fieldErrorClass}>
					{error}
				</p>
			)}
		</div>
	);
}

/** The fine-grained PAT, and what it must be scoped to. */
export function AccessTokenField({
	id,
	errorId,
	error,
	inputProps,
}: {
	id: string;
	errorId: string;
	error?: string;
	inputProps: ComponentProps<typeof Input>;
}) {
	return (
		<div className="space-y-2">
			<Label htmlFor={id}>Access token</Label>
			<Input
				id={id}
				aria-invalid={!!error}
				aria-describedby={error ? errorId : undefined}
				type="password"
				placeholder="PAT or GitHub App installation token"
				{...inputProps}
			/>
			<p className="type-caption-plain text-muted-foreground">
				Use a fine-grained PAT restricted to this repository with Contents:
				Read-only. Your organization may require approval or SSO authorization.
				Repository and branch access are verified before saving.
			</p>
			{error && (
				<p id={errorId} className={fieldErrorClass}>
					{error}
				</p>
			)}
		</div>
	);
}

/**
 * The shared secret GitHub signs push deliveries with.
 *
 * Offered on the App and PAT paths only: a public repository is read with no
 * credential and a webhook needs administration of the repository, so the host
 * refuses a secret there and keeps the source current by periodic sync. The
 * caller decides whether to render it; this component does not re-derive that.
 */
export function WebhookSecretField({
	id,
	errorId,
	error,
	inputProps,
}: {
	id: string;
	errorId: string;
	error?: string;
	inputProps: ComponentProps<typeof Input>;
}) {
	return (
		<div className="space-y-2">
			<Label htmlFor={id}>Webhook secret (optional)</Label>
			<Input
				id={id}
				aria-invalid={!!error}
				aria-describedby={error ? errorId : undefined}
				type="password"
				placeholder="Shared secret GitHub signs push deliveries with"
				{...inputProps}
			/>
			<p className="type-caption-plain text-muted-foreground">
				Enables live webhook ingestion. Add it later if you don&apos;t have it
				yet.
			</p>
			{error && (
				<p id={errorId} className={fieldErrorClass}>
					{error}
				</p>
			)}
		</div>
	);
}
