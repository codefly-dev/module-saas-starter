"use client";

import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import type { ReactNode } from "react";
import type { ProductFeature } from "@/lib/product-features";
import { usePublicRuntimeConfig } from "@/lib/public-runtime-config-provider";
import { Button, Page as PageBody, PageHeader } from "@/shared/ui";

const featureLabels: Record<ProductFeature, string> = {
	subscriptions: "Subscriptions",
	sso: "Single sign-on",
	entitlements: "Entitlements",
};

export function OptionalProductFeature({
	feature,
	children,
}: {
	feature: ProductFeature;
	children: ReactNode;
}) {
	const { productFeatures } = usePublicRuntimeConfig();
	if (productFeatures[feature]) return children;
	return (
		<PageBody>
			<PageHeader
				title={`${featureLabels[feature]} is not enabled`}
				description="This optional feature is turned off for this application. Contact your application administrator to enable it."
			>
				<Button
					variant="outline"
					size="sm"
					nativeButton={false}
					render={<Link href="/admin" />}
				>
					<ArrowLeft className="mr-1 h-4 w-4" />
					Back to administration
				</Button>
			</PageHeader>
		</PageBody>
	);
}
