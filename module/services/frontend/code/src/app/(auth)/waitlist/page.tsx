import type { Metadata } from "next";
import { Suspense } from "react";
import { WaitlistPage } from "@/features/waitlist/ui/waitlist-page";

export const metadata: Metadata = { title: "Request access" };

export default function Page() {
	return (
		<Suspense>
			<WaitlistPage />
		</Suspense>
	);
}
