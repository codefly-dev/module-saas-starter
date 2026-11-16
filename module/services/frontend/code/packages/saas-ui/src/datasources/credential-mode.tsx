"use client";

import {
	Button,
	Input,
	Label,
	Radio,
	RadioGroup,
} from "@codefly-dev/ui/layout";
import type { ComponentProps, ReactNode } from "react";

/**
 * How a GitHub source authenticates, and the fields each mode needs.
 *
 * Lifted out of `connect-github-form.tsx` so a surface that fixes the
 * repository — `<DeclaredSourceCard>`, where the solution declares what it
 * reads and the person supplies only the credential — asks for the credential
 * in exactly the same words, with exactly the same three modes, without
 * copying the form. Two surfaces that say the three ways differently teach two
 * different products, so the choice and its one-line explanations live here
 * once.
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
 * Narrows a chosen value to a mode the caller can actually offer. The App
 * option is absent when the client cannot drive App onboarding, so a value of
 * `app` arriving from anywhere else falls back to the token path rather than
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

/**
 * The three ways to connect, as equal choices, each said in one line. The
 * order is the order a person meets them in GitHub's own settings: a token
 * they already hold, the app an owner installs, or nothing at all.
 */
const methods: { value: CredentialMethod; label: string; help: string }[] = [
	{
		value: "pat",
		label: "Access token",
		help: "Paste a fine-grained token with read access to the repository.",
	},
	{
		value: "app",
		label: "GitHub app",
		help: "Install it on the organization; no token to manage, syncs on every push.",
	},
	{
		value: "public",
		label: "Public repository",
		help: "No credential needed.",
	},
];

/** How to connect: one radio card per way, with the line that explains it. */
export function CredentialMethodField({
	id,
	value,
	appAvailable,
	onChange,
}: {
	id: string;
	/** Undefined reads as the token path, which is what an unset mode means. */
	value: CredentialMethod | undefined;
	appAvailable: boolean;
	onChange: (next: CredentialMethod) => void;
}) {
	return (
		<RadioGroup
			id={id}
			aria-label="How to connect"
			value={value ?? "pat"}
			onValueChange={(next) =>
				onChange(credentialMethodFrom(String(next), appAvailable))
			}
			className="grid gap-2 sm:grid-cols-3"
		>
			{methods
				.filter((method) => method.value !== "app" || appAvailable)
				.map((method) => (
					<Label
						key={method.value}
						className="cursor-pointer items-start gap-3 rounded-lg border p-3 has-[[data-checked]]:border-primary has-[[data-checked]]:bg-muted/40"
					>
						<Radio value={method.value} className="mt-0.5" />
						<span className="space-y-0.5">
							<span className="block type-label">{method.label}</span>
							<span className="block type-caption-plain text-muted-foreground">
								{method.help}
							</span>
						</span>
					</Label>
				))}
		</RadioGroup>
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
						: "Install the GitHub app"}
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
				aria-describedby={error ? errorId : `${id}-help`}
				type="password"
				autoComplete="new-password"
				spellCheck={false}
				{...inputProps}
			/>
			<p id={`${id}-help`} className="type-caption-plain text-muted-foreground">
				Give it read-only access to this repository&apos;s contents. It is checked
				before saving and never shown again.
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
 * Offered on the token path only: a public repository is read with no
 * credential and a webhook needs administration of the repository, so the host
 * refuses a secret there; an App-connected source receives its pushes through
 * the App and holds no secret of its own. The caller decides whether to render
 * it; this component does not re-derive that.
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
				aria-describedby={error ? errorId : `${id}-help`}
				type="password"
				autoComplete="new-password"
				spellCheck={false}
				{...inputProps}
			/>
			<p id={`${id}-help`} className="type-caption-plain text-muted-foreground">
				Lets GitHub send each push as it happens. Without it, the source still
				syncs on its schedule.
			</p>
			{error && (
				<p id={errorId} className={fieldErrorClass}>
					{error}
				</p>
			)}
		</div>
	);
}
