"use client";

import { createClient } from "@connectrpc/connect";
import { CheckCircle2, Loader2 } from "lucide-react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useCallback, useEffect, useState } from "react";
import { TurnstileWidget } from "@/components/turnstile-widget";
import {
	AcquisitionMode,
	WaitlistService,
} from "@/gen/saas/accounts/v1/waitlist_pb";
import { apiTransport } from "@/lib/connect/transport";
import { configuredAbuseProtection } from "@/lib/abuse-protection";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	Checkbox,
	Input,
	Label,
	Textarea,
} from "@/shared/ui";

const client = createClient(WaitlistService, apiTransport);

export function WaitlistPage() {
	const params = useSearchParams();
	const [mode, setMode] = useState<AcquisitionMode>();
	const [submitting, setSubmitting] = useState(false);
	const [message, setMessage] = useState("");
	const [error, setError] = useState("");
	const [policyVersion, setPolicyVersion] = useState("");
	const [turnstileToken, setTurnstileToken] = useState("");
	const [turnstileAttempt, setTurnstileAttempt] = useState(0);
	const abuseProtection = configuredAbuseProtection(
		process.env.NEXT_PUBLIC_ABUSE_PROTECTION_MODE,
		process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY,
	);
	const updateTurnstileToken = useCallback((token: string) => {
		setTurnstileToken(token);
	}, []);

	useEffect(() => {
		client
			.getAcquisitionStatus({})
			.then((status) => {
				setMode(status.mode);
				setPolicyVersion(status.consentPolicyVersion);
			})
			.catch(() => setError("Access status is temporarily unavailable."));
	}, []);

	async function submit(form: FormData) {
		setSubmitting(true);
		setError("");
		try {
			const response = await client.join({
				email: String(form.get("email") ?? ""),
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
			setMessage(response.message);
		} catch {
			setTurnstileToken("");
			setTurnstileAttempt((attempt) => attempt + 1);
			setError(
				"We could not submit this request. Check your connection and try again.",
			);
		} finally {
			setSubmitting(false);
		}
	}

	if (message) {
		return (
			<div className="flex min-h-screen items-center justify-center p-4">
				<Card className="w-full max-w-md text-center">
					<CardHeader>
						<CheckCircle2 className="mx-auto h-12 w-12 text-primary" />
						<CardTitle>Request received</CardTitle>
						<CardDescription>{message}</CardDescription>
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
			</div>
		);
	}

	if (mode === AcquisitionMode.OPEN_SIGNUP) {
		return (
			<div className="flex min-h-screen items-center justify-center p-4">
				<Card className="w-full max-w-md text-center">
					<CardHeader>
						<CardTitle>Registration is open</CardTitle>
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
			</div>
		);
	}

	if (mode === AcquisitionMode.CLOSED) {
		return (
			<div className="flex min-h-screen items-center justify-center p-4">
				<Card className="w-full max-w-md text-center">
					<CardHeader>
						<CardTitle>Registration is closed</CardTitle>
						<CardDescription>
							We are not accepting access requests right now.
						</CardDescription>
					</CardHeader>
				</Card>
			</div>
		);
	}

	return (
		<div className="flex min-h-screen items-center justify-center bg-muted/20 p-4">
			<Card className="w-full max-w-lg">
				<CardHeader>
					<CardTitle>Request access</CardTitle>
					<CardDescription>
						Join the waitlist. If email verification is enabled, we will send a
						time-limited confirmation link.
					</CardDescription>
				</CardHeader>
				<CardContent>
					<form
						id="waitlist-form"
						className="space-y-4"
						action={submit}
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
						<div className="space-y-2">
							<Label htmlFor="email">Work email</Label>
							<Input
								id="email"
								name="email"
								type="email"
								required
								autoComplete="email"
							/>
						</div>
						<div className="grid gap-4 sm:grid-cols-2">
							<div className="space-y-2">
								<Label htmlFor="name">Name</Label>
								<Input id="name" name="name" autoComplete="name" />
							</div>
							<div className="space-y-2">
								<Label htmlFor="company">Company</Label>
								<Input
									id="company"
									name="company"
									autoComplete="organization"
								/>
							</div>
						</div>
						<div className="space-y-2">
							<Label htmlFor="useCase">What would you like to build?</Label>
							<Textarea id="useCase" name="useCase" maxLength={2000} />
						</div>
						<div className="flex items-start gap-2 text-sm">
							<Checkbox id="marketing" name="marketing" />
							<Label htmlFor="marketing" className="font-normal">
								I agree to receive optional product updates.
							</Label>
						</div>
						<p className="text-xs text-muted-foreground">
							See our{" "}
							<Link className="underline" href="/legal/privacy">
								Privacy Policy
							</Link>
							.
						</p>
						<TurnstileWidget
							key={turnstileAttempt}
							action="waitlist_join"
							onTokenChange={updateTurnstileToken}
						/>
						{error && (
							<p
								id="waitlist-error"
								role="alert"
								className="text-sm text-destructive"
							>
								{error}
							</p>
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
						{submitting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
						Join waitlist
					</Button>
				</CardFooter>
			</Card>
		</div>
	);
}
