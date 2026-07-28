"use client";

import {
	AlertCircle,
	CheckCircle2,
	LogIn,
	RefreshCw,
	UserRoundCog,
} from "lucide-react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { useAuth } from "@/lib/auth";
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

type State =
	| { kind: "loading" }
	| { kind: "ready"; emailHint?: string }
	| { kind: "error"; message: string; wrongAccount?: boolean }
	| { kind: "success"; organizationId: string; organizationName: string };

const UUID =
	/^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function failureMessage(status: number): {
	message: string;
	wrongAccount?: boolean;
} {
	if (status === 401) {
		return { message: "Sign in with the invited email address to continue." };
	}
	if (status === 403) {
		return {
			message:
				"This account does not match the invited email address. Switch accounts and try again.",
			wrongAccount: true,
		};
	}
	if (status === 409 || status === 429) {
		return {
			message:
				"The organization cannot add another member right now. Ask an administrator to review its seats.",
		};
	}
	if (status >= 500) {
		return {
			message:
				"The invitation service is temporarily unavailable. No membership changes were made.",
		};
	}
	return {
		message:
			"This invitation is invalid, expired, revoked, or no longer available. Ask the inviter for a new link.",
	};
}

export function InvitationAcceptance() {
	const params = useSearchParams();
	const invitationId = params.get("id");
	const safeInvitationId =
		invitationId && UUID.test(invitationId) ? invitationId : undefined;
	const router = useRouter();
	const {
		isAuthenticated,
		isLoading: authLoading,
		getToken,
		switchOrganization,
		logout,
	} = useAuth();
	const [state, setState] = useState<State>({ kind: "loading" });

	useEffect(() => {
		let cancelled = false;
		fetch("/invitations/accept/api", {
			method: "POST",
			cache: "no-store",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({
				action: "inspect",
				invitationId: safeInvitationId,
			}),
		})
			.then(async (response) => {
				if (!response.ok) throw failureMessage(response.status);
				const summary = (await response.json()) as { emailHint?: string };
				if (!cancelled) {
					setState({ kind: "ready", emailHint: summary.emailHint });
				}
			})
			.catch((error) => {
				if (!cancelled) {
					setState({
						kind: "error",
						message:
							typeof error?.message === "string"
								? error.message
								: failureMessage(400).message,
					});
				}
			});
		return () => {
			cancelled = true;
		};
	}, [safeInvitationId]);

	async function accept() {
		setState({ kind: "loading" });
		const token = getToken();
		const response = await fetch("/invitations/accept/api", {
			method: "POST",
			cache: "no-store",
			headers: {
				"Content-Type": "application/json",
				...(token ? { Authorization: `Bearer ${token}` } : {}),
			},
			body: JSON.stringify({
				action: "accept",
				invitationId: safeInvitationId,
			}),
		});
		if (!response.ok) {
			setState({ kind: "error", ...failureMessage(response.status) });
			return;
		}
		const result = (await response.json()) as {
			organization?: { id?: string; name?: string };
		};
		if (!result.organization?.id) {
			setState({ kind: "error", ...failureMessage(500) });
			return;
		}
		await switchOrganization(result.organization.id);
		setState({
			kind: "success",
			organizationId: result.organization.id,
			organizationName: result.organization.name ?? "your organization",
		});
	}

	const returnPath = safeInvitationId
		? `/invitations/accept?id=${encodeURIComponent(safeInvitationId)}`
		: "/invitations/accept";

	if (state.kind === "loading" || authLoading) {
		return (
			<div className="flex min-h-screen items-center justify-center p-4">
				<Card className="w-full max-w-md">
					<CardHeader>
						<Skeleton className="h-7 w-44" />
						<Skeleton className="h-4 w-full" />
					</CardHeader>
					<CardContent>
						<Skeleton className="h-11 w-full" />
					</CardContent>
				</Card>
			</div>
		);
	}

	if (state.kind === "success") {
		return (
			<div className="flex min-h-screen items-center justify-center p-4">
				<Card className="w-full max-w-md text-center">
					<CardHeader>
						<CheckCircle2 className="mx-auto h-12 w-12 text-primary" />
						<CardTitle>Invitation accepted</CardTitle>
						<CardDescription>
							You are now a member of {state.organizationName}.
						</CardDescription>
					</CardHeader>
					<CardFooter className="justify-center">
						<Button onClick={() => router.replace("/onboarding")}>
							Continue to workspace
						</Button>
					</CardFooter>
				</Card>
			</div>
		);
	}

	return (
		<div className="flex min-h-screen items-center justify-center bg-muted/20 p-4">
			<Card className="w-full max-w-md">
				<CardHeader>
					<CardTitle>Organization invitation</CardTitle>
					<CardDescription>
						{state.kind === "ready" && state.emailHint
							? `This invitation was sent to ${state.emailHint}.`
							: "Accept using the same primary email address that received the invitation."}
					</CardDescription>
				</CardHeader>
				<CardContent>
					{state.kind === "error" && (
						<div
							role="alert"
							className="flex gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm"
						>
							<AlertCircle className="h-4 w-4 shrink-0 text-destructive" />
							{state.message}
						</div>
					)}
				</CardContent>
				<CardFooter className="flex-col gap-2">
					{isAuthenticated ? (
						<>
							<Button className="w-full" onClick={accept}>
								Accept invitation
							</Button>
							{state.kind === "error" && state.wrongAccount && (
								<Button
									variant="outline"
									className="w-full"
									onClick={async () => {
										await logout();
										router.replace(
											`/auth/login?next=${encodeURIComponent(returnPath)}`,
										);
									}}
								>
									<UserRoundCog className="mr-2 h-4 w-4" />
									Switch account
								</Button>
							)}
						</>
					) : (
						<Button
							className="w-full"
							nativeButton={false}
							render={
								<Link
									href={`/auth/login?next=${encodeURIComponent(returnPath)}`}
								/>
							}
						>
							<LogIn className="mr-2 h-4 w-4" />
							Sign in or create account
						</Button>
					)}
					{state.kind === "error" && !state.wrongAccount && (
						<Button
							variant="ghost"
							className="w-full"
							onClick={() => window.location.reload()}
						>
							<RefreshCw className="mr-2 h-4 w-4" />
							Retry
						</Button>
					)}
				</CardFooter>
			</Card>
		</div>
	);
}
