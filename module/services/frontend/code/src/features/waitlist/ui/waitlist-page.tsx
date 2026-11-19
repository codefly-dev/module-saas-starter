"use client";

import { createClient } from "@connectrpc/connect";
import { CheckCircle2 } from "lucide-react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import { TurnstileWidget } from "@/components/turnstile-widget";
import {
	AcquisitionMode,
	WaitlistService,
} from "@/gen/saas/accounts/v1/waitlist_pb";
import { configuredAbuseProtection } from "@/lib/abuse-protection";
import { apiTransport } from "@/lib/connect/transport";
import { usePublicRuntimeConfig } from "@/lib/public-runtime-config-provider";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	Checkbox,
	ErrorState,
	Field,
	Input,
	Label,
	Page,
	Spinner,
	Textarea,
	useLoadingPhase,
} from "@/shared/ui";

const client = createClient(WaitlistService, apiTransport);

export function WaitlistPage() {
	const params = useSearchParams();
	const [mode, setMode] = useState<AcquisitionMode>();
	const [submitting, setSubmitting] = useState(false);
	const [received, setReceived] = useState(false);
	const [statusError, setStatusError] = useState(false);
	const [statusAttempt, setStatusAttempt] = useState(0);
	const [emailError, setEmailError] = useState("");
	const pending = useRef(false);
	const statusLoading = useLoadingPhase(mode === undefined && !statusError);
	const [error, setError] = useState("");
	const [policyVersion, setPolicyVersion] = useState("");
	const [turnstileToken, setTurnstileToken] = useState("");
	const [turnstileAttempt, setTurnstileAttempt] = useState(0);
	const runtimeConfig = usePublicRuntimeConfig();
	const abuseProtection = configuredAbuseProtection(
		runtimeConfig.abuseProtection.mode,
		runtimeConfig.abuseProtection.siteKey,
	);
	const updateTurnstileToken = useCallback((token: string) => {
		setTurnstileToken(token);
	}, []);

	useEffect(() => {
		let active = true;
		setStatusError(false);
		client
			.getAcquisitionStatus({})
			.then((status) => {
				if (!active) return;
				if (
					![
						AcquisitionMode.OPEN_SIGNUP,
						AcquisitionMode.INVITE_ONLY,
						AcquisitionMode.APPROVAL_REQUIRED,
						AcquisitionMode.CLOSED,
					].includes(status.mode) ||
					((status.mode === AcquisitionMode.INVITE_ONLY ||
						status.mode === AcquisitionMode.APPROVAL_REQUIRED) &&
						!status.consentPolicyVersion)
				) {
					setStatusError(true);
					return;
				}
				setMode(status.mode);
				setPolicyVersion(status.consentPolicyVersion);
			})
			.catch(() => {
				if (active) setStatusError(true);
			});
		return () => {
			active = false;
		};
	}, [statusAttempt]);

	async function submit(form: FormData) {
		if (
			pending.current ||
			(mode !== AcquisitionMode.INVITE_ONLY &&
				mode !== AcquisitionMode.APPROVAL_REQUIRED) ||
			!policyVersion
		)
			return;
		const email = String(form.get("email") ?? "").trim();
		if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
			setEmailError("Enter a valid email address.");
			return;
		}
		setEmailError("");
		pending.current = true;
		setSubmitting(true);
		setError("");
		try {
			await client.join({
				email,
				name: String(form.get("name") ?? ""),
				company: String(form.get("company") ?? ""),
				useCase: String(form.get("useCase") ?? ""),
				website: String(form.get("website") ?? ""),
				source: params.get("source") ?? "",
				campaign: params.get("campaign") ?? "",
				referrer: document.referrer.slice(0, 2048),
				referralCode: params.get("ref") ?? "",
				marketingConsent: form.get("marketing") === "on",
				policyVersion,
				turnstileToken,
			});
			setReceived(true);
		} catch {
			setTurnstileToken("");
			setTurnstileAttempt((attempt) => attempt + 1);
			setError(
				"We could not submit this request. Check your connection and try again.",
			);
		} finally {
			pending.current = false;
			setSubmitting(false);
		}
	}

	if (
		mode === undefined ||
		statusError ||
		statusLoading.indicator ||
		statusLoading.quiet
	) {
		return (
			<Page className="min-h-screen flex items-center justify-center">
				{statusError ? (
					<Card className="w-full max-w-lg">
						<CardContent className="space-y-4 pt-6">
							<ErrorState
								title="Couldn’t check registration availability"
								detail="Check your connection and try again."
							/>
							<Button onClick={() => setStatusAttempt((value) => value + 1)}>
								Try again
							</Button>
						</CardContent>
					</Card>
				) : statusLoading.indicator ? (
					<Spinner label="Checking registration availability" />
				) : null}
			</Page>
		);
	}
	if (received) {
		return (
			<Page className="flex min-h-screen items-center justify-center p-4">
				<Card className="w-full max-w-md text-center">
					<CardHeader>
						<CheckCircle2 className="mx-auto h-12 w-12 text-primary" />
						<CardTitle role="heading" aria-level={1}>
							Request received
						</CardTitle>
						<CardDescription>
							Your request has been received. Check your inbox for any next
							steps.
						</CardDescription>
					</CardHeader>
					<CardFooter className="justify-center">
						<Button
							variant="outline"
							nativeButton={false}
							render={<Link href="/" />}
						>
							Return home
						</Button>
					</CardFooter>
				</Card>
			</Page>
		);
	}

	if (mode === AcquisitionMode.OPEN_SIGNUP) {
		return (
			<Page className="flex min-h-screen items-center justify-center p-4">
				<Card className="w-full max-w-md text-center">
					<CardHeader>
						<CardTitle role="heading" aria-level={1}>
							Registration is open
						</CardTitle>
						<CardDescription>
							You do not need to join a waitlist.
						</CardDescription>
					</CardHeader>
					<CardFooter className="justify-center">
						<Button nativeButton={false} render={<Link href="/auth/login" />}>
							Create account
						</Button>
					</CardFooter>
				</Card>
			</Page>
		);
	}

	if (mode === AcquisitionMode.CLOSED) {
		return (
			<Page className="flex min-h-screen items-center justify-center p-4">
				<Card className="w-full max-w-md text-center">
					<CardHeader>
						<CardTitle role="heading" aria-level={1}>
							Registration is closed
						</CardTitle>
						<CardDescription>
							We are not accepting access requests right now.
						</CardDescription>
					</CardHeader>
				</Card>
			</Page>
		);
	}

	return (
		<Page className="flex min-h-screen items-center justify-center bg-muted/20 p-4">
			<Card className="w-full max-w-lg">
				<CardHeader>
					<CardTitle role="heading" aria-level={1}>
						Request access
					</CardTitle>
					<CardDescription>
						Join the waitlist. If email verification is enabled, we will send a
						time-limited confirmation link.
					</CardDescription>
				</CardHeader>
				<CardContent>
					<form
						id="waitlist-form"
						className="space-y-4"
						noValidate
						onSubmit={(event) => {
							event.preventDefault();
							void submit(new FormData(event.currentTarget));
						}}
						aria-describedby={error ? "waitlist-error" : undefined}
					>
						<div className="hidden" aria-hidden="true">
							<Label htmlFor="website">Website</Label>
							<Input
								id="website"
								name="website"
								tabIndex={-1}
								autoComplete="off"
							/>
						</div>
						<Field label="Work email" required error={emailError || undefined}>
							{(control) => (
								<Input
									{...control}
									name="email"
									type="email"
									autoComplete="email"
									disabled={submitting}
									onChange={() => setEmailError("")}
								/>
							)}
						</Field>
						<div className="grid gap-4 sm:grid-cols-2">
							<Field label="Name" description="Optional">
								{(control) => (
									<Input
										{...control}
										name="name"
										autoComplete="name"
										disabled={submitting}
									/>
								)}
							</Field>
							<Field label="Company" description="Optional">
								{(control) => (
									<Input
										{...control}
										name="company"
										autoComplete="organization"
										disabled={submitting}
									/>
								)}
							</Field>
						</div>
						<Field
							label="What would you like to build?"
							description="Optional. Up to 2,000 characters."
						>
							{(control) => (
								<Textarea
									{...control}
									name="useCase"
									maxLength={2000}
									disabled={submitting}
								/>
							)}
						</Field>
						<div className="flex items-start gap-2 text-sm">
							<Checkbox id="marketing" name="marketing" disabled={submitting} />
							<Label htmlFor="marketing" className="font-normal">
								I agree to receive optional product updates.
							</Label>
						</div>
						<p className="text-xs text-muted-foreground">
							See our{" "}
							<Link className="underline" href="/legal/privacy">
								Privacy policy
							</Link>
							.
						</p>
						<TurnstileWidget
							key={turnstileAttempt}
							action="waitlist_join"
							onTokenChange={updateTurnstileToken}
						/>
						{error && (
							<div id="waitlist-error">
								<ErrorState title="Couldn’t request access" detail={error} />
							</div>
						)}
					</form>
				</CardContent>
				<CardFooter>
					<Button
						type="submit"
						form="waitlist-form"
						className="w-full"
						disabled={
							submitting ||
							mode === undefined ||
							!policyVersion ||
							(abuseProtection.enabled && !turnstileToken)
						}
					>
						{submitting ? "Requesting access…" : "Join waitlist"}
					</Button>
				</CardFooter>
			</Card>
		</Page>
	);
}
