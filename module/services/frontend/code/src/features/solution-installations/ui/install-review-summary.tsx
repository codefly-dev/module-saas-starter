"use client";

import {
	type AbilityRole,
	article,
	RoleAbilitiesList,
	roleAbilities,
	updateAsks,
} from "@codefly-dev/saas-ui/solution";
import { useState } from "react";
import type { ReviewedOffer } from "@/gen/host/installation/v1/installation_pb";
import type { ReviewedSupportingModule } from "@/gen/saas/accounts/v1/installations_pb";
import { Button } from "@/shared/ui";
import { reachDetail, supportingSummary } from "../model/install-review";
import { DeclaredOperations } from "./declared-access";

/** What each declared role lets people do, one card per role. */
export function RoleReach({
	offer,
}: {
	offer: Pick<ReviewedOffer, "roles" | "target">;
}) {
	if (!offer.roles.length) return null;
	return (
		<section aria-label="Roles" className="space-y-3">
			<h3 className="font-medium">Roles</h3>
			{roleAbilities(offer.target, offer.roles).map((abilities) => (
				<section
					key={abilities.role}
					aria-label={`Role ${abilities.role}`}
					className="space-y-2 rounded-lg border p-3"
				>
					<h4 className="font-medium">
						What {article(abilities.role).toLowerCase()} {abilities.role} can do
					</h4>
					<RoleAbilitiesList abilities={abilities} />
				</section>
			))}
		</section>
	);
}

/**
 * What an update asks beyond what was approved, role by role, in the same
 * words as the roles themselves. `approved` is the review's approved reach;
 * an approval recorded before reach was kept has none to compare with.
 */
export function UpdateAsks({
	offer,
	approved,
}: {
	offer: Pick<ReviewedOffer, "roles" | "target">;
	approved: readonly AbilityRole[];
}) {
	const asks = approved.length
		? updateAsks(offer.target, offer.roles, approved)
		: [];
	return (
		<section
			aria-label="What this update adds"
			className="space-y-2 rounded-lg border border-warning/40 bg-warning/5 p-3"
		>
			<h3 className="font-medium">What this update adds</h3>
			{!approved.length ? (
				<p>
					What you approved earlier was not recorded in detail, so everything
					each role can do is shown below.
				</p>
			) : !asks.length ? (
				<p>
					Nothing a role can do changes; the release itself asks for more. See
					the technical details.
				</p>
			) : (
				asks.map((role) => (
					<div key={role.role}>
						<p className="font-medium">
							{article(role.role)} {role.role} would also be able to
						</p>
						<RoleAbilitiesList
							abilities={role}
							label={`What the update adds for ${article(role.role).toLowerCase()} ${role.role}`}
						/>
					</div>
				))
			)}
			<p className="text-muted-foreground">
				Until you update, everyone keeps exactly the access you approved.
			</p>
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
			{!modules.length && <p>It needs no other module.</p>}
			{installs.length > 0 && (
				<p>
					Also installs: {installs.join(", ")} —{" "}
					{standing.length
						? `each acting on its own with: ${standing.join(", ")}`
						: "none acts on its own"}
				</p>
			)}
			{reused.length > 0 && (
				<p>Already installed, used as is: {reused.join(", ")}</p>
			)}
			{optional.length > 0 && (
				<p>Optional, not installed: {optional.join(", ")}</p>
			)}
			{modules.length > 0 && (
				<p className="text-muted-foreground">
					Uninstalling it later keeps the modules other solutions share.
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
	offer: Pick<ReviewedOffer, "ceiling" | "roles">;
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
		<section aria-label="Technical details" className="space-y-2">
			<Button
				type="button"
				variant="ghost"
				size="sm"
				aria-expanded={open}
				onClick={() => setOpen(!open)}
			>
				{open ? "Hide technical details" : "Technical details"}
			</Button>
			{open && (
				<div className="space-y-3">
					<DeclaredOperations offer={offer} />
					{reachDetail(offer).length > 0 && (
						<div className="space-y-1">
							<p className="font-medium">What each role reaches</p>
							<ul className="list-disc pl-5">
								{reachDetail(offer).map((line) => (
									<li key={line}>{line}</li>
								))}
							</ul>
						</div>
					)}
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
