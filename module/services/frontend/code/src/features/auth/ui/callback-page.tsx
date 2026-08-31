"use client";

import { AlertCircle, Loader2 } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useState } from "react";
import { useAuth } from "@/lib/auth";
import { AuthError, operatorReference } from "@/lib/auth-errors";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
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
	// The identity provider's own error (or a malformed callback URL) is already
	// specific and actionable, so it is surfaced verbatim with no reference line.
	const parameterError = useMemo<CallbackFailure | null>(() => {
		if (providerError) {
			return {
				message: `${providerError}${providerErrorDescription ? `: ${providerErrorDescription}` : ""}`,
				reference: null,
			};
		}
		if (!code || !state) {
			return {
				message:
					"This sign-in link is missing its code or state. Please start over.",
				reference: null,
			};
		}
		return null;
	}, [providerError, providerErrorDescription, code, state]);

	useEffect(() => {
		if (parameterError || !code || !state) return;

		completeOAuth(code, state).catch((e) => {
			if (e instanceof AuthError) {
				setCompletionFailure({
					message: e.message,
					reference: operatorReference(e.detail),
				});
				return;
			}
			// State/CSRF guards and other pre-exchange checks throw a plain Error
			// whose message is already user-facing.
			setCompletionFailure({
				message: e instanceof Error ? e.message : "Sign-in failed.",
				reference: null,
			});
		});
	}, [code, state, parameterError, completeOAuth]);

	const failure = parameterError ?? completionFailure;

	if (failure) {
		return (
			<div className="flex min-h-screen items-center justify-center bg-background px-4">
				<Card className="w-full max-w-md">
					<CardHeader className="text-center">
						<CardTitle className="text-xl">Sign-in failed</CardTitle>
						<CardDescription>
							We couldn&apos;t finish signing you in.
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<div className="flex items-center gap-2 rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm text-destructive">
							<AlertCircle className="h-4 w-4 shrink-0" />
							{failure.message}
						</div>
						{failure.reference && (
							<div className="rounded-md border bg-muted/40 p-3 text-xs text-muted-foreground">
								<span className="font-medium">Reference: </span>
								<span className="font-mono break-all">{failure.reference}</span>
							</div>
						)}
						<a href="/auth/login" className="w-full">
							<Button variant="outline" className="w-full">
								Try again
							</Button>
						</a>
					</CardContent>
				</Card>
			</div>
		);
	}

	return (
		<div className="flex min-h-screen items-center justify-center bg-background px-4">
			<Card className="w-full max-w-md">
				<CardHeader className="text-center">
					<CardTitle className="text-xl flex items-center justify-center gap-2">
						<Loader2 className="h-5 w-5 animate-spin" />
						Signing you in
					</CardTitle>
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
				<div className="flex min-h-screen items-center justify-center bg-background px-4">
					<Card className="w-full max-w-md">
						<CardContent className="flex items-center justify-center py-12">
							<Skeleton className="h-8 w-48" />
						</CardContent>
					</Card>
				</div>
			}
		>
			<CallbackHandler />
		</Suspense>
	);
}
