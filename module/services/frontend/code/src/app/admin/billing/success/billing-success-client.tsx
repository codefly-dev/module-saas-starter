"use client";

import Link from "next/link";
import { Button, Page as PageBody, PageHeader, Panel } from "@/shared/ui";

export default function BillingSuccessClient() {
	return (
		<PageBody>
			<PageHeader
				title="Review your subscription"
				description="Check billing for your current plan and payment status."
			/>
			<Panel className="space-y-4">
				<p className="text-muted-foreground">
					If you just completed checkout, your subscription may take a moment to
					update.
				</p>
				<Button role="link" nativeButton={false} render={<Link href="/admin/billing" />}>
					View billing
				</Button>
			</Panel>
		</PageBody>
	);
}
