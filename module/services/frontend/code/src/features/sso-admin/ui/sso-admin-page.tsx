"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	AlertCircle,
	ArrowUpRight,
	CheckCircle2,
	KeyRound,
	Lightbulb,
	ShieldOff,
} from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	Skeleton,
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

	const { data: cfg, isLoading } = useQuery(ssoQueries.status(orgId));

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
				toast.error("Setup failed", {
					description: "No portal link returned",
				});
				return;
			}
			// Navigate the whole page rather than open a new tab — the
			// post-setup return_url bounces back here, which only works
			// cleanly with a same-tab flow. Opens in new tab confuses the
			// session continuity the operator expects.
			setRedirecting(true);
			window.location.href = resp.portalLink;
		},
		onError: (err) => toast.error("Setup failed", { description: err.message }),
	});

	const disable = useMutation({
		mutationFn: () => ssoMutations.disable(orgId),
		onSuccess: () => {
			toast.success("SSO disabled");
			void queryClient.invalidateQueries({
				queryKey: ["sso", "status", orgId],
			});
		},
		onError: (err) =>
			toast.error("Disable failed", { description: err.message }),
	});

	function handleDisable() {
		const ok = window.confirm(
			"Disable SSO for this organization?\n\n" +
				"Users will fall back to password / OAuth sign-in. Re-enabling " +
				"skips the IdP-setup step (we keep your WorkOS organization id), " +
				"but any new email-domain users won't auto-route until then.",
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
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Single Sign-On</h1>
					<p className="text-muted-foreground">
						Wire your IdP (Okta, Azure AD, Google Workspace, etc.) to route
						users in your domain through SAML / OIDC. Self-serve via the WorkOS
						Admin Portal.
					</p>
				</div>
				<OrgSelector />
			</div>

			{!orgId ? (
				<EmptyState
					icon={KeyRound}
					title="Select an organization"
					description="SSO connections are per-org. Pick an organization above to view or set up its SSO configuration."
				/>
			) : isLoading ? (
				<Skeleton className="h-48 w-full" />
			) : (
				<Card>
					<CardHeader>
						<CardTitle className="flex items-center justify-between text-base">
							<span>SSO status</span>
							{isActive && (
								<Badge variant="default" className="bg-emerald-600">
									Active
								</Badge>
							)}
							{isLinked && <Badge variant="secondary">Setup pending</Badge>}
							{isDisabled && <Badge variant="outline">Disabled</Badge>}
							{isUnconfigured && (
								<Badge variant="outline">Not configured</Badge>
							)}
						</CardTitle>
					</CardHeader>
					<CardContent className="space-y-4">
						{isActive && cfg && (
							<div className="rounded-md border border-emerald-500/30 bg-emerald-500/5 p-4 flex items-start gap-3">
								<CheckCircle2 className="h-5 w-5 text-emerald-600 shrink-0 mt-0.5" />
								<div className="space-y-1">
									<div className="font-medium text-sm">
										SSO is live for this organization
									</div>
									<div className="text-xs text-muted-foreground">
										Provider: <code>{cfg.provider}</code> • Configured{" "}
										{cfg.configuredAt
											? formatDate(
													new Date(
														Number(cfg.configuredAt.seconds) * 1000,
													).toISOString(),
												)
											: "—"}
									</div>
									<div className="text-xs text-muted-foreground">
										Connection id:{" "}
										<code className="font-mono">{cfg.connectionId}</code>
									</div>
								</div>
							</div>
						)}

						{isLinked && (
							<div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-4 flex items-start gap-3">
								<AlertCircle className="h-5 w-5 text-amber-600 shrink-0 mt-0.5" />
								<div className="space-y-1">
									<div className="font-medium text-sm">
										IdP configuration pending
									</div>
									<div className="text-xs text-muted-foreground">
										Your admin portal session was created but the IdP setup
										isn&apos;t finished yet. Re-open the portal to continue.
									</div>
								</div>
							</div>
						)}

						{isUnconfigured && (
							<div className="rounded-md border bg-muted/30 p-4 text-sm flex items-start gap-3">
								<Lightbulb className="h-5 w-5 text-muted-foreground shrink-0 mt-0.5" />
								<div className="space-y-1">
									<div className="font-medium">No SSO configured</div>
									<div className="text-xs text-muted-foreground">
										Click &quot;Set up SSO&quot; to open the WorkOS admin portal
										— you&apos;ll upload your IdP&apos;s SAML metadata or OIDC
										config there. Takes about 5 minutes.
									</div>
								</div>
							</div>
						)}

						{isDisabled && (
							<div className="rounded-md border bg-muted/30 p-4 text-sm flex items-start gap-3">
								<ShieldOff className="h-5 w-5 text-muted-foreground shrink-0 mt-0.5" />
								<div className="space-y-1">
									<div className="font-medium">SSO is disabled</div>
									<div className="text-xs text-muted-foreground">
										Re-enable to send users back through your existing IdP — we
										kept the WorkOS org id, so no re-upload of metadata is
										required unless your IdP changed.
									</div>
								</div>
							</div>
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
		</div>
	);
}
