"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { productFeatures, type ProductFeature } from "@/lib/product-features";

const featureLabels: Record<ProductFeature, string> = {
	subscriptions: "Subscriptions",
	sso: "Single Sign-On",
	entitlements: "Entitlements",
};

export function OptionalProductFeature({
	feature,
	children,
}: {
	feature: ProductFeature;
	children: ReactNode;
}) {
	if (productFeatures[feature]) return children;
	return (
		<div className="space-y-3">
			<h1 className="text-2xl font-bold">
				{featureLabels[feature]} is not enabled
			</h1>
			<p className="text-muted-foreground">
				This optional feature is turned off for this application. Contact your
				application administrator to enable it.
			</p>
			<Link href="/admin" className="text-primary hover:underline">
				Back to administration
			</Link>
		</div>
	);
}
