"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Check, Loader2 } from "lucide-react";
import { useAuth } from "@/lib/auth";
import {
  PRICING_PLANS,
  type PricingPlan,
} from "@/features/billing/core/plans";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
  Button,
  Badge,
} from "@/shared/ui";
import { cn } from "@/shared/lib/utils";

const API_BASE = process.env.NEXT_PUBLIC_BACKEND_URL || "http://localhost:8080";

export function PricingPage() {
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
    const subject = encodeURIComponent(
      `Enterprise inquiry - ${plan.displayName}`,
    );
    window.location.href = `mailto:${email}?subject=${subject}`;
  }

  return (
    <div className="mx-auto max-w-5xl px-4 py-16">
      <div className="text-center mb-12">
        <h1 className="text-4xl font-bold tracking-tight">Pricing</h1>
        <p className="mt-4 text-lg text-muted-foreground">
          Choose the plan that fits your team. All plans include a 14-day free
          trial.
        </p>
      </div>

      {error && (
        <div className="mb-8 rounded-md border border-destructive/50 bg-destructive/10 p-4 text-sm text-destructive text-center">
          {error}
        </div>
      )}

      <div className="grid grid-cols-1 gap-6 md:grid-cols-3">
        {PRICING_PLANS.map((plan) => (
          <Card
            key={plan.name}
            className={cn(
              "relative flex flex-col",
              plan.highlighted && "border-primary shadow-lg",
            )}
          >
            {plan.highlighted && (
              <Badge className="absolute -top-3 left-1/2 -translate-x-1/2">
                Most Popular
              </Badge>
            )}

            <CardHeader>
              <CardTitle className="text-xl">{plan.displayName}</CardTitle>
              <CardDescription>{plan.description}</CardDescription>
            </CardHeader>

            <CardContent className="flex-1">
              <div className="mb-6">
                <span className="text-4xl font-bold">{plan.priceLabel}</span>
                {plan.cta !== "contact" && (
                  <span className="text-muted-foreground"> / month</span>
                )}
              </div>

              <ul className="space-y-3">
                {plan.features.map((f) => (
                  <li key={f} className="flex items-start gap-2 text-sm">
                    <Check className="h-4 w-4 mt-0.5 shrink-0 text-primary" />
                    <span>{f}</span>
                  </li>
                ))}
              </ul>
            </CardContent>

            <CardFooter>
              {plan.cta === "start_free" && (
                <Button variant="outline" className="w-full" disabled>
                  Current Plan
                </Button>
              )}
              {plan.cta === "upgrade" && (
                <Button
                  className="w-full"
                  onClick={() => handleUpgrade(plan)}
                  disabled={loadingPlan === plan.name}
                >
                  {loadingPlan === plan.name ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      Starting checkout...
                    </>
                  ) : (
                    "Upgrade"
                  )}
                </Button>
              )}
              {plan.cta === "contact" && (
                <Button
                  variant="outline"
                  className="w-full"
                  onClick={() => handleContact(plan)}
                >
                  Contact Sales
                </Button>
              )}
            </CardFooter>
          </Card>
        ))}
      </div>
    </div>
  );
}
