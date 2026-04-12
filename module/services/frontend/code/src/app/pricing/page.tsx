"use client";

// Pricing page.
//
// For authenticated users, the "Upgrade" button POSTs to the backend
// /v1/billing/checkout endpoint (through the gateway) and redirects
// the browser to the returned Stripe-hosted checkout URL.
//
// For unauthenticated users, "Upgrade" first bounces through
// /auth/login with ?next=/pricing so they come back here after
// signing in.
//
// The pricing data lives in the pure features/billing/core/plans.ts
// module — this file is just the container.

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/lib/auth";
import { PRICING_PLANS, type PricingPlan } from "@/features/billing/core/plans";

const API_BASE = process.env.NEXT_PUBLIC_BACKEND_URL || "http://localhost:8080";

export default function PricingPage() {
  const { isAuthenticated, accessToken } = useAuth();
  const router = useRouter();
  const [loadingPlan, setLoadingPlan] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function handleUpgrade(plan: PricingPlan) {
    setError(null);
    if (!isAuthenticated) {
      router.push(`/auth/login?next=/pricing`);
      return;
    }
    setLoadingPlan(plan.name);
    try {
      const res = await fetch(`${API_BASE}/v1/billing/checkout`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${accessToken}`,
        },
        body: JSON.stringify({
          plan_name: plan.name,
          success_url: `${window.location.origin}/billing/success`,
          cancel_url: `${window.location.origin}/pricing`,
          trial_days: 14,
        }),
      });
      if (!res.ok) {
        const body = await res.text().catch(() => "");
        throw new Error(body || `checkout failed: ${res.status}`);
      }
      const data = (await res.json()) as { url: string };
      window.location.href = data.url;
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not start checkout");
      setLoadingPlan(null);
    }
  }

  function handleContact(plan: PricingPlan) {
    const email = encodeURIComponent("sales@localhost");
    const subject = encodeURIComponent(`Enterprise inquiry — ${plan.displayName}`);
    window.location.href = `mailto:${email}?subject=${subject}`;
  }

  return (
    <div
      style={{
        maxWidth: "1100px",
        margin: "3rem auto",
        padding: "0 1.5rem",
        fontFamily: "system-ui, sans-serif",
      }}
    >
      <h1 style={{ fontSize: "2rem", marginBottom: "0.5rem" }}>Pricing</h1>
      <p style={{ color: "#666", marginBottom: "2rem" }}>
        Choose the plan that fits your team. All plans include a 14-day free trial.
      </p>

      {error && (
        <div style={{ background: "#fee", color: "#c00", padding: "1rem", borderRadius: "0.5rem", marginBottom: "1rem" }}>
          {error}
        </div>
      )}

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(3, 1fr)",
          gap: "1.5rem",
        }}
      >
        {PRICING_PLANS.map((plan) => (
          <div
            key={plan.name}
            style={{
              border: plan.highlighted ? "2px solid #0066cc" : "1px solid #ddd",
              borderRadius: "0.75rem",
              padding: "1.5rem",
              background: plan.highlighted ? "#f5faff" : "white",
            }}
          >
            <h2 style={{ fontSize: "1.25rem", marginBottom: "0.25rem" }}>
              {plan.displayName}
            </h2>
            <p style={{ fontSize: "1.75rem", fontWeight: 600, margin: "0.5rem 0" }}>
              {plan.priceLabel}
            </p>
            <p style={{ color: "#666", fontSize: "0.9rem", marginBottom: "1rem" }}>
              {plan.description}
            </p>

            <ul style={{ listStyle: "none", padding: 0, margin: "0 0 1.5rem 0" }}>
              {plan.features.map((f) => (
                <li key={f} style={{ padding: "0.25rem 0", fontSize: "0.9rem" }}>
                  ✓ {f}
                </li>
              ))}
            </ul>

            {plan.cta === "start_free" && (
              <button
                disabled
                style={{
                  width: "100%",
                  padding: "0.75rem",
                  background: "#eee",
                  border: "1px solid #ccc",
                  borderRadius: "0.5rem",
                  color: "#888",
                  cursor: "not-allowed",
                }}
              >
                Current
              </button>
            )}
            {plan.cta === "upgrade" && (
              <button
                onClick={() => handleUpgrade(plan)}
                disabled={loadingPlan === plan.name}
                style={{
                  width: "100%",
                  padding: "0.75rem",
                  background: "#0066cc",
                  color: "white",
                  border: "none",
                  borderRadius: "0.5rem",
                  cursor: loadingPlan === plan.name ? "wait" : "pointer",
                  fontSize: "1rem",
                }}
              >
                {loadingPlan === plan.name ? "Starting checkout…" : "Upgrade"}
              </button>
            )}
            {plan.cta === "contact" && (
              <button
                onClick={() => handleContact(plan)}
                style={{
                  width: "100%",
                  padding: "0.75rem",
                  background: "white",
                  border: "1px solid #0066cc",
                  color: "#0066cc",
                  borderRadius: "0.5rem",
                  cursor: "pointer",
                  fontSize: "1rem",
                }}
              >
                Contact sales
              </button>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
