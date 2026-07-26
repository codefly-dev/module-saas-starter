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
	const { mutateAsync: verifyMagicLink } = useVerifyMagicLink();
	const token = params.get("token");
	const [verificationError, setVerificationError] = useState<string | null>(
		null,
	);

	useEffect(() => {
		if (!token) return;

		void verifyMagicLink({ token })
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
				setVerificationError(
					e instanceof Error ? e.message : "Verification failed",
				);
			});
	}, [setTokensFromMagicLink, token, verifyMagicLink]);

	const error = token ? verificationError : "Missing token in URL";
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

export default function MagicLinkClient() {
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
