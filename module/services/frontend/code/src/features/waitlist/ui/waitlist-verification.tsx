"use client";

import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import {
	Button,
	Card,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
} from "@/shared/ui";

export function WaitlistVerification() {
	const [message, setMessage] = useState("");
	const [loading, setLoading] = useState(true);
	const [succeeded, setSucceeded] = useState(false);

	useEffect(() => {
		fetch("/waitlist/verify/api", { method: "POST", cache: "no-store" })
			.then(async (response) => {
				const body = (await response.json()) as {
					message?: string;
					state?: string;
				};
				setSucceeded(
					response.ok &&
						[
							"WAITLIST_STATE_VERIFIED",
							"WAITLIST_STATE_APPROVED",
							"WAITLIST_STATE_INVITED",
							"WAITLIST_STATE_CONVERTED",
						].includes(body.state ?? ""),
				);
				setMessage(
					body.message ??
						(response.ok
							? "Your email is verified."
							: "This verification link is invalid or no longer available."),
				);
			})
			.catch(() => {
				setSucceeded(false);
				setMessage("Verification is temporarily unavailable. Try again.");
			})
			.finally(() => setLoading(false));
	}, []);

	return (
		<div className="flex min-h-screen items-center justify-center p-4">
			<Card className="w-full max-w-md text-center">
				<CardHeader>
					{loading ? (
						<Loader2 className="mx-auto h-10 w-10 animate-spin" />
					) : succeeded ? (
						<CheckCircle2
							aria-label="Verification succeeded"
							className="mx-auto h-10 w-10 text-primary"
						/>
					) : (
						<AlertCircle
							aria-label="Verification failed"
							className="mx-auto h-10 w-10 text-destructive"
						/>
					)}
					<CardTitle>
						{loading ? "Verifying email" : "Waitlist status"}
					</CardTitle>
					<CardDescription aria-live="polite">{message}</CardDescription>
				</CardHeader>
				{!loading && (
					<CardFooter className="justify-center">
						<Button
							nativeButton={false}
							render={<Link href="/waitlist" />}
							variant="outline"
						>
							Return to waitlist
						</Button>
					</CardFooter>
				)}
			</Card>
		</div>
	);
}
