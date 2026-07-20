"use client";

import { AlertCircle, Loader2 } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState } from "react";
import { useVerifyMagicLink } from "@/features/auth/service/mutations";
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

function MagicLinkHandler() {
	const params = useSearchParams();
	const { setTokensFromMagicLink } = useAuth();
	const verifyMagicLink = useVerifyMagicLink();
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		const token = params.get("token");
		if (!token) {
			setError("Missing token in URL");
			return;
		}

		verifyMagicLink
			.mutateAsync({ token })
			.then((data) => {
				const authenticated = setTokensFromMagicLink(
					data.accessToken,
					data.refreshToken,
					data.user?.uuid,
					data.mfaRequired ? data.mfaToken : undefined,
				);
				if (authenticated) window.location.replace("/");
			})
			.catch((e) => {
				setError(e instanceof Error ? e.message : "Verification failed");
			});
		// Run once on mount
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, []);

	if (error) {
		return (
			<div className="flex min-h-screen items-center justify-center bg-background px-4">
				<Card className="w-full max-w-md">
					<CardHeader className="text-center">
						<CardTitle className="text-xl">Sign-in failed</CardTitle>
						<CardDescription>
							The magic link is invalid or has expired.
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<div className="flex items-center gap-2 rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm text-destructive">
							<AlertCircle className="h-4 w-4 shrink-0" />
							{error}
						</div>
						<a href="/auth/login" className="w-full block">
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
						Verifying magic link
					</CardTitle>
					<CardDescription>Please wait while we sign you in.</CardDescription>
				</CardHeader>
			</Card>
		</div>
	);
}

export default function MagicLinkPage() {
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
			<MagicLinkHandler />
		</Suspense>
	);
}
