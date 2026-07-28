import type { Metadata } from "next";
import { Suspense } from "react";
import { InvitationAcceptance } from "@/features/invitations/ui/invitation-acceptance";

export const metadata: Metadata = {
	title: "Accept invitation",
	referrer: "no-referrer",
};

export default function Page() {
	return (
		<Suspense>
			<InvitationAcceptance />
		</Suspense>
	);
}
