"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, KeyRound, Lightbulb, ShieldOff } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Banner,
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Skeleton,
	Stack,
} from "@/shared/ui";
import { ssoMutations } from "../service/mutations";
import { ssoQueries } from "../service/queries";

/**
 * SSOAdminPage — per-org self-serve SSO setup. Three states the page
 * presents based on api Get response:
 *
 *  - empty (provider="" or status=""): "Set up SSO" CTA. Click →
 *    StartSetup → redirect to WorkOS Admin Portal.
 *  - linked (status="linked"): IdP setup pending. Show portal-relink
 *    button + "Disable" / "Restart setup" actions.
 *  - active (status="active"): Show connection_id, configured_at,
 *    "Disable" button. Optional re-portal link to update IdP.
 *  - disabled (status="disabled"): Show greyed status + "Re-enable"
 *    CTA that re-runs StartSetup against the preserved WorkOS org.
 */
export function SSOAdminPage() {
	const queryClient = useQueryClient();
	const { organizationId: orgId = "" } = useAuth();
	const [redirecting, setRedirecting] = useState(false);

	const {
		data: cfg,
		isLoading,
		isError,
		error,
		refetch,
	} = useQuery(ssoQueries.status(orgId));
	// A refusal is not cured by asking again, so only a read that did not come
	// back offers the retry.
	const refused = readOutcome(isError, error) === "forbidden";

	const start = useMutation({
		mutationFn: () => {
			const returnUrl =
				typeof window !== "undefined"
					? `${window.location.origin}/admin/sso`
					: "";
			return ssoMutations.startSetup(orgId, returnUrl);
		},
		onSuccess: (resp) => {
			if (!resp.portalLink) {
				toast.error(failureText("start the SSO setup", undefined));
				return;
			}
			// Navigate the whole page rather than open a new tab — the
			// post-setup return_url bounces back here, which only works
			// cleanly with a same-tab flow. Opens in new tab confuses the
			// session continuity the operator expects.
			setRedirecting(true);
			window.location.href = resp.portalLink;
		},
		onError: (error) => toast.error(failureText("start the SSO setup", error)),
	});

	const disable = useMutation({
		mutationFn: () => ssoMutations.disable(orgId),
		onSuccess: () => {
			toast.success("SSO disabled");
			void queryClient.invalidateQueries({
				queryKey: ["sso", "status", orgId],
			});
		},
		onError: (error) => toast.error(failureText("disable SSO", error)),
	});

	function handleDisable() {
		const ok = window.confirm(
			"Disable SSO for this organization?\n\n" +
				"People will sign in with a password or a social account instead. " +
				"You can re-enable it later without setting up your identity " +
				"provider again.",
		);
		if (ok) disable.mutate();
	}

	// status reads — the api returns "" for never-configured.
	const status = cfg?.status ?? "";
	const isActive = status === "active";
	const isLinked = status === "linked";
	const isDisabled = status === "disabled";
	const isUnconfigured = !status;

	return (
		<PageBody>
			<PageHeader
				title="Single sign-on"
				description="Let the people of your organization sign in through your company's identity provider, such as Okta, Microsoft Entra ID or Google Workspace."
				actions={<OrgSelector />}
			/>

			{!orgId ? (
				<EmptyState
					icon={KeyRound}
					title="Select an organization"
					description="Each organization has its own single sign-on. Pick an organization above to see or set up its connection."
				/>
			) : isLoading ? (
				<Skeleton className="h-48 w-full" />
			) : isError ? (
				// A failed read is not "never set up": offering "Set up SSO" over a
				// connection that may already be live invites replacing it.
				<Panel>
					<Stack gap={3} align="start">
						<ErrorState
							title="Couldn't load the single sign-on status"
							detail={
								refused
									? "You don't have permission to see this organization's single sign-on. Ask one of its administrators for access."
									: "Whether single sign-on is set up is unknown. Try again; if it keeps failing, tell your administrator."
							}
						/>
						{!refused && (
							<Button
								variant="outline"
								size="sm"
								onClick={() => void refetch()}
							>
								Try again
							</Button>
						)}
					</Stack>
				</Panel>
			) : (
				<Card>
					<CardHeader>
						<CardTitle className="flex items-center justify-between text-base">
							<span>SSO status</span>
							{isActive && (
								<Badge tone="success" dot>
									Active
								</Badge>
							)}
							{isLinked && (
								<Badge tone="warning" dot>
									Setup pending
								</Badge>
							)}
							{isDisabled && <Badge tone="neutral">Disabled</Badge>}
							{isUnconfigured && <Badge tone="neutral">Not set up</Badge>}
						</CardTitle>
					</CardHeader>
					<CardContent className="space-y-4">
						{isActive && cfg && (
							<Banner tone="success" title="SSO is live for this organization">
								<div>
									Provider: {cfg.provider} • Set up{" "}
									{cfg.configuredAt
										? formatDate(
												new Date(
													Number(cfg.configuredAt.seconds) * 1000,
												).toISOString(),
											)
										: "—"}
								</div>
								<div>
									Connection ID:{" "}
									<span className="select-all font-mono text-xs">
										{cfg.connectionId}
									</span>
								</div>
							</Banner>
						)}

						{isLinked && (
							<Banner
								tone="warning"
								title="Identity provider setup isn't finished"
							>
								The setup was started but not completed. Continue it to turn
								single sign-on on.
							</Banner>
						)}

						{isUnconfigured && (
							<Banner
								tone="info"
								icon={<Lightbulb />}
								title="Single sign-on isn't set up"
							>
								Set up SSO opens a guided setup where you enter your identity
								provider&apos;s details. It takes about five minutes.
							</Banner>
						)}

						{isDisabled && (
							<Banner tone="info" icon={<ShieldOff />} title="SSO is disabled">
								Re-enable it to send people back through your identity provider.
								Its details were kept, so you only enter them again if your
								provider changed.
							</Banner>
						)}

						<div className="flex items-center justify-end gap-2">
							{(isUnconfigured || isLinked || isDisabled) && (
								<Button
									onClick={() => start.mutate()}
									disabled={start.isPending || redirecting}
								>
									<ArrowUpRight className="mr-2 h-4 w-4" />
									{start.isPending || redirecting
										? "Redirecting…"
										: isLinked
											? "Continue setup"
											: isDisabled
												? "Re-enable SSO"
												: "Set up SSO"}
								</Button>
							)}

							{(isActive || isLinked) && (
								<Button
									variant="outline"
									onClick={handleDisable}
									disabled={disable.isPending}
									className="text-destructive border-destructive/30 hover:bg-destructive/10"
								>
									<ShieldOff className="mr-2 h-4 w-4" />
									{disable.isPending ? "Disabling…" : "Disable SSO"}
								</Button>
							)}
						</div>
					</CardContent>
				</Card>
			)}
		</PageBody>
	);
}
