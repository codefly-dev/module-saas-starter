"use client";

import { useState } from "react";
import { toast } from "sonner";
import { useAuth } from "@/lib/auth";
import { Button } from "@/shared/ui";
import { UserLabel } from "./user-label";

export function ImpersonationBanner() {
	const { impersonation, user, exitImpersonation } = useAuth();
	const [minimized, setMinimized] = useState(false);
	const [exiting, setExiting] = useState(false);

	if (!impersonation.isImpersonating) return null;

	if (minimized) {
		return (
			<Button
				type="button"
				variant="outline"
				onClick={() => setMinimized(false)}
				className="fixed bottom-4 right-4 z-[9999] bg-warning/10 text-foreground px-3 py-1.5 rounded-full text-xs font-bold shadow-lg hover:bg-warning/20 transition-colors"
				aria-label="Expand impersonation banner"
			>
				Impersonating
			</Button>
		);
	}

	return (
		<div className="fixed bottom-0 left-0 right-0 z-[9999] bg-warning/10 text-foreground border-t-2 border-warning">
			<div className="flex flex-wrap gap-3 items-center justify-between px-4 py-2 max-w-screen-xl mx-auto">
				<div className="flex flex-wrap items-center gap-2 text-sm">
					<span className="font-bold">Impersonation active</span>
					<span>&mdash;</span>
					{/* The subject is who the session acts as; the signed-in admin is
					    who it stays attributable to. Naming the admin here as the
					    person "viewed as" is the identity confusion this banner
					    exists to make visible. */}
					<span>
						Viewing as{" "}
						<strong>
							<UserLabel
								userId={impersonation.subjectId ?? ""}
								fallback="Selected person"
							/>
						</strong>
					</span>
					<span className="text-muted-foreground">
						(signed in as {user?.email || "your administrator account"})
					</span>
				</div>
				<div className="flex items-center gap-2">
					<Button
						type="button"
						variant="outline"
						onClick={() => setMinimized(true)}
						className="text-foreground text-xs underline"
					>
						Minimize
					</Button>
					<Button
						type="button"
						variant="outline"
						onClick={() => {
							setExiting(true);
							void exitImpersonation()
								// Exiting fails loudly rather than silently stranding the
								// operator in the target's session: a transient restore
								// failure leaves impersonation active and retryable.
								.catch(() =>
									toast.error(
										"Could not end the impersonation session. Try again; if it keeps failing, contact your administrator.",
									),
								)
								.finally(() => setExiting(false));
						}}
						disabled={exiting}
						className="bg-primary text-primary-foreground px-3 py-1 rounded text-sm font-medium hover:bg-primary/90 transition-colors disabled:opacity-60"
					>
						{exiting ? "Exiting…" : "Stop impersonating"}
					</Button>
				</div>
			</div>
		</div>
	);
}
