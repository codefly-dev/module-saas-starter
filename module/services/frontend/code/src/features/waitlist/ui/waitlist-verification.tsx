"use client";

import { CheckCircle2, Loader2 } from "lucide-react";
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

	useEffect(() => {
		fetch("/waitlist/verify/api", { method: "POST", cache: "no-store" })
			.then(async (response) => {
				const body = (await response.json()) as { message?: string };
				setMessage(
					body.message ??
						(response.ok
							? "Your email is verified."
							: "This verification link is invalid or no longer available."),
				);
			})
			.catch(() =>
				setMessage("Verification is temporarily unavailable. Try again."),
			)
			.finally(() => setLoading(false));
	}, []);

	return (
		<div className="flex min-h-screen items-center justify-center p-4">
			<Card className="w-full max-w-md text-center">
				<CardHeader>
					{loading ? (
						<Loader2 className="mx-auto h-10 w-10 animate-spin" />
					) : (
						<CheckCircle2 className="mx-auto h-10 w-10 text-primary" />
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
