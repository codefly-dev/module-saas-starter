"use client";

import { useSearchParams } from "next/navigation";
import type { ReactNode } from "react";
import {
  primaryAcquisitionHandoff,
  productHandoff,
  type AttributionInput,
} from "@/lib/cta";

function currentAttribution(searchParams: URLSearchParams): AttributionInput {
  return Object.fromEntries(searchParams.entries());
}

export function AttributionHandoffLink({
  children,
  className,
  destination,
  plan,
}: {
  children: ReactNode;
  className: string;
  destination: "login" | "signup" | "waitlist";
  plan?: string;
}) {
  const attribution = currentAttribution(useSearchParams());
  const href = productHandoff(destination, attribution, plan);
  return href ? (
    <a className={className} href={href}>
      {children}
    </a>
  ) : null;
}

export function AttributionAcquisitionActions() {
  const attribution = currentAttribution(useSearchParams());
  const handoff = primaryAcquisitionHandoff(attribution);
  const login = productHandoff("login", attribution);
  return (
    <div className="button-row">
      {handoff.available && handoff.href ? (
        <a className="button" href={handoff.href}>
          {handoff.label}
        </a>
      ) : (
        <span className="button button-disabled" aria-disabled="true">
          {handoff.label} unavailable
        </span>
      )}
      {login && handoff.href !== login ? (
        <a className="button button-quiet" href={login}>
          Sign in
        </a>
      ) : null}
    </div>
  );
}
