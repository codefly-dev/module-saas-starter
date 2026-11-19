"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useAuth } from "@/lib/auth";
import { AuthError } from "@/lib/auth-errors";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	ErrorState,
	Page,
	PageHeader,
	Skeleton,
	Spinner,
} from "@/shared/ui";

interface CallbackFailure {
	message: string;
	/** Support request reference, when known. */
	reference: string | null;
	denied?: boolean;
}

function CallbackHandler() {
	const { completeOAuth } = useAuth();
	const params = useSearchParams();
	const [completionFailure, setCompletionFailure] =
		useState<CallbackFailure | null>(null);
	const code = params.get("code");
	const state = params.get("state");
	const providerError = params.get("error");

	// The identity provider's own error is a code and a description written for
	// whoever integrated it, and both arrive in the address bar, where anyone can
	// put anything. Show only this page’s explanation, never the supplied text.
	const parameterError = useMemo<CallbackFailure | null>(() => {
		if (providerError) {
			return {
				message:
					"Your sign-in provider did not sign you in. The sign-in was cancelled, or your account is not allowed to use this application.",
				reference: null,
			};
		}
		if (!code || !state) {
			return {
				message: "This sign-in link is incomplete. Start again.",
				reference: null,
			};
		}
		return null;
	}, [providerError, code, state]);

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
					message:
						e.detail.status === 403 || e.detail.code === "permission_denied"
							? "You don’t have access to this application. Ask your organization administrator to grant access."
							: e.message,
					denied:
						e.detail.status === 403 || e.detail.code === "permission_denied",
					reference:
						e.detail.requestId &&
						/^[a-zA-Z0-9_-]{1,128}$/.test(e.detail.requestId)
							? e.detail.requestId
							: null,
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
			<Page className="flex min-h-screen w-full items-center justify-center p-4">
				<Card className="w-full max-w-md">
					<CardHeader>
						<PageHeader title="Sign-in failed" />
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
					{!failure.denied && (
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
					)}
				</Card>
			</Page>
		);
	}

	return (
		<Page className="flex min-h-screen w-full items-center justify-center p-4">
			<Card className="w-full max-w-md">
				<CardHeader>
					<PageHeader title="Signing you in" />
					<Spinner label="Completing sign-in" />
					<CardDescription>
						Please wait while we complete the sign-in process.
					</CardDescription>
				</CardHeader>
			</Card>
		</Page>
	);
}

export function CallbackPage() {
	return (
		<Suspense
			fallback={
				<Page className="flex min-h-screen w-full items-center justify-center p-4">
					<Card className="w-full max-w-md">
						<CardHeader>
							<Skeleton className="h-7 w-44" />
							<Skeleton className="h-4 w-full" />
						</CardHeader>
					</Card>
				</Page>
			}
		>
			<CallbackHandler />
		</Suspense>
	);
}
