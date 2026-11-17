"use client";

import { Loader2 } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useAuth } from "@/lib/auth";
import { AuthError, operatorReference } from "@/lib/auth-errors";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	ErrorState,
	Skeleton,
} from "@/shared/ui";

interface CallbackFailure {
	message: string;
	/** Operator-facing correlation line (code / request id / trace id), when known. */
	reference: string | null;
}

function CallbackHandler() {
	const { completeOAuth } = useAuth();
	const params = useSearchParams();
	const [completionFailure, setCompletionFailure] =
		useState<CallbackFailure | null>(null);
	const code = params.get("code");
	const state = params.get("state");
	const providerError = params.get("error");
	const providerErrorDescription = params.get("error_description");
	// The identity provider's own error is a code and a description written for
	// whoever integrated it, and both arrive in the address bar, where anyone can
	// put anything. The person is told what happened in this page's words; what
	// the provider said goes on the reference line, for whoever they ask.
	const parameterError = useMemo<CallbackFailure | null>(() => {
		if (providerError) {
			return {
				message:
					"Your sign-in provider did not sign you in. The sign-in was cancelled, or your account is not allowed to use this application.",
				reference: `${providerError}${providerErrorDescription ? `: ${providerErrorDescription}` : ""}`,
			};
		}
		if (!code || !state) {
			return {
				message: "This sign-in link is incomplete. Start again.",
				reference: null,
			};
		}
		return null;
	}, [providerError, providerErrorDescription, code, state]);

	// An authorization code is redeemable once. completeOAuth's identity changes
	// as sign-in stores its tokens, which re-runs this effect with the same code;
	// the second run found the one-shot state already cleared and flashed
	// "Sign-in failed" until the first run's navigation landed.
	const redeemed = useRef<string | null>(null);

	useEffect(() => {
		if (parameterError || !code || !state) return;
		if (redeemed.current === code) return;
		redeemed.current = code;

		completeOAuth(code, state).catch((e) => {
			if (e instanceof AuthError) {
				setCompletionFailure({
					message: e.message,
					reference: operatorReference(e.detail),
				});
				return;
			}
			// Anything else — the checks made before the exchange, a request that
			// never left the browser — is worded for developers. The remedy is the
			// same for all of them, so that is what is said.
			console.error("Sign-in could not be completed", e);
			setCompletionFailure({
				message:
					"This sign-in could not be completed, and nothing was changed. Start again from the sign-in page.",
				reference: null,
			});
		});
	}, [code, state, parameterError, completeOAuth]);

	const failure = parameterError ?? completionFailure;

	if (failure) {
		return (
			<div className="flex min-h-screen w-full items-center justify-center p-4">
				<Card className="w-full max-w-md">
					<CardHeader>
						<h1 data-slot="page-title" className="type-page-title">
							Sign-in failed
						</h1>
						<CardDescription>
							We couldn&apos;t finish signing you in.
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<ErrorState title={failure.message} />
						{failure.reference && (
							<p className="text-xs text-muted-foreground">
								<span className="font-medium">Reference: </span>
								<span className="font-mono break-all">{failure.reference}</span>
							</p>
						)}
					</CardContent>
					<CardFooter>
						<Button
							variant="outline"
							size="lg"
							className="w-full"
							nativeButton={false}
							render={<a href="/auth/login" />}
						>
							Sign in again
						</Button>
					</CardFooter>
				</Card>
			</div>
		);
	}

	return (
		<div className="flex min-h-screen w-full items-center justify-center p-4">
			<Card className="w-full max-w-md">
				<CardHeader>
					<h1
						data-slot="page-title"
						className="type-page-title flex items-center gap-2"
					>
						<Loader2 className="size-5 animate-spin" />
						Signing you in
					</h1>
					<CardDescription>
						Please wait while we complete the sign-in process.
					</CardDescription>
				</CardHeader>
			</Card>
		</div>
	);
}

export function CallbackPage() {
	return (
		<Suspense
			fallback={
				<div className="flex min-h-screen w-full items-center justify-center p-4">
					<Card className="w-full max-w-md">
						<CardHeader>
							<Skeleton className="h-7 w-44" />
							<Skeleton className="h-4 w-full" />
						</CardHeader>
					</Card>
				</div>
			}
		>
			<CallbackHandler />
		</Suspense>
	);
}
