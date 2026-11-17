"use client";

// Post-checkout success landing. Stripe redirects here with a
// session_id query param. The subscription webhook has (or will
// very soon) set the user's plan; we just show a confirmation and
// bounce them into the app.

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { Button, Page as PageBody, PageHeader, Panel } from "@/shared/ui";

export default function BillingSuccessClient() {
	const router = useRouter();

	useEffect(() => {
		const timer = setTimeout(() => router.push("/"), 3000);
		return () => clearTimeout(timer);
	}, [router]);

	return (
		<PageBody>
			<Panel className="p-6">
				<PageHeader
					title="Subscription active"
					description="Thanks for subscribing. Your account has been upgraded and you'll be taken to the dashboard shortly."
					actions={
						<Button nativeButton={false} render={<Link href="/" />}>
							Go to dashboard
						</Button>
					}
				/>
			</Panel>
		</PageBody>
	);
}
