"use client";

import { useState } from "react";
import type { ReviewedOffer } from "@/gen/host/installation/v1/installation_pb";
import type { ReviewedSupportingModule } from "@/gen/saas/accounts/v1/installations_pb";
import { Button } from "@/shared/ui";
import { roleReach, supportingSummary } from "../model/install-review";
import { DeclaredOperations } from "./declared-access";

/** What each declared role lets people do, in plain words, grouped by where. */
export function RoleReach({
	offer,
}: {
	offer: Pick<ReviewedOffer, "roles" | "target">;
}) {
	if (!offer.roles.length) return null;
	return (
		<section aria-label="What people can do" className="space-y-2">
			<h3 className="font-medium">What people can do</h3>
			{roleReach(offer).map(({ where, roles }) => (
				<div key={where}>
					<p>{where}:</p>
					<ul className="list-disc pl-5">
						{roles.map(({ role, text }) => (
							<li key={role}>
								<span className="font-medium">{role}</span>: {text}
							</li>
						))}
					</ul>
				</div>
			))}
		</section>
	);
}

/** The supporting modules, one line each for what is installed and reused. */
export function SupportingModules({
	modules,
}: {
	modules: readonly ReviewedSupportingModule[];
}) {
	const { installs, standing, reused, optional } = supportingSummary(modules);
	return (
		<section aria-label="Supporting modules" className="space-y-1">
			<h3 className="font-medium">Supporting modules</h3>
			{!modules.length && <p>No supporting modules are required.</p>}
			{installs.length > 0 && (
				<p>
					Also installs: {installs.join(", ")} —{" "}
					{standing
						? "each with its own consent record"
						: "no standing machine access"}
				</p>
			)}
			{reused.length > 0 && (
				<p>Already installed and reused: {reused.join(", ")}</p>
			)}
			{optional.length > 0 && (
				<p>Optional, not installed: {optional.join(", ")}</p>
			)}
			{modules.length > 0 && (
				<p className="text-muted-foreground">
					Supporting modules keep their own installation and revocation records.
					Uninstalling this solution keeps shared modules installed.
				</p>
			)}
		</section>
	);
}

/**
 * Everything the consent covers, technically, behind one disclosure: the
 * declared operations and, per module installed with this review, its access
 * grouped and merged. A reused module's consent is unchanged, so it has none.
 */
export function TechnicalAccess({
	offer,
	modules,
}: {
	offer: Pick<ReviewedOffer, "ceiling">;
	modules: readonly {
		module: ReviewedSupportingModule;
		access: readonly string[];
	}[];
}) {
	const [open, setOpen] = useState(false);
	const installed = modules.filter(
		({ module }) => module.required && !module.reuseInstallationId,
	);
	return (
		<section aria-label="Technical access" className="space-y-2">
			<Button
				type="button"
				variant="ghost"
				size="sm"
				aria-expanded={open}
				onClick={() => setOpen(!open)}
			>
				{open ? "Hide technical access" : "Show technical access"}
			</Button>
			{open && (
				<div className="space-y-3">
					<DeclaredOperations offer={offer} />
					{installed.map(({ module, access }) => (
						<div key={module.principal} className="space-y-1">
							<p className="font-medium">
								{module.target || module.principal}
								{module.release && ` — ${module.release}`}
							</p>
							<ul className="list-disc pl-5">
								{access.map((line) => (
									<li key={line}>{line}</li>
								))}
							</ul>
						</div>
					))}
				</div>
			)}
		</section>
	);
}
