"use client";

// Post-checkout success landing. Stripe redirects here with a
// session_id query param. The subscription webhook has (or will
// very soon) set the user's plan; we just show a confirmation and
// bounce them into the app.

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect } from "react";

export default function BillingSuccessClient() {
	const router = useRouter();

	useEffect(() => {
		const timer = setTimeout(() => router.push("/"), 3000);
		return () => clearTimeout(timer);
	}, [router]);

	return (
		<div
			style={{
				maxWidth: "560px",
				margin: "6rem auto",
				padding: "2rem",
				textAlign: "center",
				fontFamily: "system-ui, sans-serif",
			}}
		>
			<div style={{ fontSize: "3rem", marginBottom: "1rem" }}>🎉</div>
			<h1>Subscription active</h1>
			<p style={{ color: "#666", marginTop: "1rem" }}>
				Thanks for subscribing. Your account has been upgraded and you&apos;ll
				be redirected to the dashboard shortly.
			</p>
			<p style={{ marginTop: "2rem" }}>
				<Link href="/" style={{ color: "#0066cc" }}>
					Go to dashboard →
				</Link>
			</p>
		</div>
	);
}
