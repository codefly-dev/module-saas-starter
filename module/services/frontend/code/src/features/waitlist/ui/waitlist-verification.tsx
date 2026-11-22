"use client";

import { CheckCircle2 } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import {
	Button,
	Card,
	CardDescription,
	CardFooter,
	CardHeader,
	ErrorState,
	Page,
	PageHeader,
	Spinner,
} from "@/shared/ui";

export function WaitlistVerification() {
	const [message, setMessage] = useState("");
	const [loading, setLoading] = useState(true);
	const [succeeded, setSucceeded] = useState(false);
	const [retryable, setRetryable] = useState(false);
	const [attempt, setAttempt] = useState(0);

	useEffect(() => {
		let live = true;
		fetch("/waitlist/verify/api", { method: "POST", cache: "no-store" })
			.then(async (response) => {
				const body = (await response.json()) as {
					message?: string;
					state?: string;
				};
				if (!live) return;
				const verified =
					response.ok &&
					[
						"WAITLIST_STATE_VERIFIED",
						"WAITLIST_STATE_APPROVED",
						"WAITLIST_STATE_INVITED",
						"WAITLIST_STATE_CONVERTED",
					].includes(body.state ?? "");
				setSucceeded(verified);
				setRetryable(response.status >= 500 || response.status === 429);
				setMessage(
					verified
						? "Your email is verified."
						: response.status >= 500 || response.status === 429
							? "We couldn’t verify your email right now. Please try again shortly."
							: "This verification link is invalid or no longer available. Request a new link to continue.",
				);
			})
			.catch(() => {
				if (!live) return;
				setSucceeded(false);
				setMessage(
					"We couldn’t confirm whether your email was verified. Open the link from your email again, or contact your administrator.",
				);
			})
			.finally(() => {
				if (live) setLoading(false);
			});
		return () => {
			live = false;
		};
	}, [attempt]);

	return (
		<Page className="flex min-h-screen items-center justify-center p-4">
			<Card className="w-full max-w-md text-center">
				<CardHeader>
					<PageHeader
						title={loading ? "Verifying email" : "Email verification"}
					/>
					{loading ? (
						<Spinner label="Verifying email" />
					) : succeeded ? (
						<>
							<CheckCircle2
								aria-label="Verification succeeded"
								className="mx-auto h-10 w-10 text-primary"
							/>
							<CardDescription>{message}</CardDescription>
						</>
					) : (
						<ErrorState
							title="Email verification needs attention"
							detail={message}
						/>
					)}
				</CardHeader>
				{!loading && (
					<CardFooter className="justify-center">
						{retryable && (
							<Button
								onClick={() => {
									setLoading(true);
									setRetryable(false);
									setAttempt((value) => value + 1);
								}}
							>
								Try again
							</Button>
						)}
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
		</Page>
	);
}
