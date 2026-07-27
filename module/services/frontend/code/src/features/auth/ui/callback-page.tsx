"use client";

import { AlertCircle, Loader2 } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState } from "react";
import { useAuth } from "@/lib/auth";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	Skeleton,
} from "@/shared/ui";

function CallbackHandler() {
	const { completeOAuth } = useAuth();
	const params = useSearchParams();
	const [completionError, setCompletionError] = useState<string | null>(null);
	const code = params.get("code");
	const state = params.get("state");
	const providerError = params.get("error");
	const providerErrorDescription = params.get("error_description");
	const parameterError = providerError
		? `${providerError}${providerErrorDescription ? `: ${providerErrorDescription}` : ""}`
		: !code || !state
			? "Missing code or state in callback URL"
			: null;

	useEffect(() => {
		if (parameterError || !code || !state) return;

		completeOAuth(code, state).catch((e) => {
			setCompletionError(e instanceof Error ? e.message : "Sign-in failed");
		});
	}, [code, state, parameterError, completeOAuth]);

	const error = parameterError ?? completionError;

	if (error) {
		return (
			<div className="flex min-h-screen items-center justify-center bg-background px-4">
				<Card className="w-full max-w-md">
					<CardHeader className="text-center">
						<CardTitle className="text-xl">Sign-in failed</CardTitle>
						<CardDescription>
							Something went wrong during authentication.
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<div className="flex items-center gap-2 rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm text-destructive">
							<AlertCircle className="h-4 w-4 shrink-0" />
							{error}
						</div>
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
