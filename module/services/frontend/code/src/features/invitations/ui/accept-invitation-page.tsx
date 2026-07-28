"use client";

import { AlertCircle, Loader2 } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense } from "react";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	Skeleton,
} from "@/shared/ui";
import { useAcceptInvitation } from "../service/mutations";

function AcceptInvitationHandler() {
	const params = useSearchParams();
	const router = useRouter();
	const token = params.get("token");
	const acceptance = useAcceptInvitation();

	const error = token
		? acceptance.error instanceof Error
			? acceptance.error.message
			: null
		: "This invitation link is missing its token.";

	return (
		<div className="flex min-h-[60vh] items-center justify-center px-4">
			<Card className="w-full max-w-md">
				<CardHeader>
					<CardTitle>Accept invitation</CardTitle>
					<CardDescription>
						Confirm that you want to join the organization associated with this
						invitation.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{error && (
						<div className="flex items-center gap-2 rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm text-destructive">
							<AlertCircle className="h-4 w-4 shrink-0" />
							{error}
						</div>
					)}
				</CardContent>
				<CardFooter>
					<Button
						className="w-full"
						disabled={!token || acceptance.isPending}
						onClick={() => {
							if (!token) return;
							acceptance.mutate(token, {
								onSuccess: () => router.replace("/"),
							});
						}}
					>
						{acceptance.isPending && (
							<Loader2 className="mr-2 h-4 w-4 animate-spin" />
						)}
						{acceptance.isPending ? "Accepting..." : "Accept invitation"}
					</Button>
				</CardFooter>
			</Card>
		</div>
	);
}

export function AcceptInvitationPage() {
	return (
		<Suspense
			fallback={
				<div className="flex min-h-[60vh] items-center justify-center px-4">
					<Skeleton className="h-64 w-full max-w-md" />
				</div>
			}
		>
			<AcceptInvitationHandler />
		</Suspense>
	);
}
