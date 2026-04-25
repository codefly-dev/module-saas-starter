/**
 * EmptyState — the "you have no X yet" panel used anywhere a list
 * resolves to zero rows. Replaces blank space with a soft illustrated
 * card + CTA so the page never looks broken.
 *
 * Conventions:
 *   - icon is the universal hint (lucide). Pick one that signals the
 *     resource type, not "empty" — same icon as the sidebar nav for
 *     that resource.
 *   - title is short ("No webhooks yet"), description is the WHY
 *     and what to do next ("Subscribe a URL...").
 *   - action is optional — primary CTA when there's something the
 *     user can do right now, omitted when the empty state is purely
 *     informational ("no audit events match your filter").
 *
 * Visual: a subtle gradient ring + dotted border evokes the "blank
 * canvas" look without an actual asset, keeping the bundle tiny.
 */

import { ReactNode } from "react";
import { cn } from "@/lib/utils";

interface EmptyStateProps {
  icon: React.ComponentType<{ className?: string }>;
  title: string;
  description?: string;
  action?: ReactNode;
  className?: string;
}

export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
  className,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        // dotted border + soft gradient = "intentional blank space"
        "relative overflow-hidden rounded-2xl border border-dashed bg-gradient-to-br from-muted/30 to-muted/0",
        "flex flex-col items-center justify-center text-center px-6 py-16",
        className,
      )}
    >
      {/* Soft glow behind the icon — gives depth without an image. */}
      <div className="relative mb-4">
        <div
          aria-hidden
          className="absolute inset-0 -z-10 rounded-full blur-2xl bg-gradient-to-br from-violet-500/15 via-indigo-500/15 to-fuchsia-500/15"
        />
        <div className="h-14 w-14 rounded-2xl bg-background border flex items-center justify-center shadow-sm">
          <Icon className="h-6 w-6 text-muted-foreground" />
        </div>
      </div>

      <h3 className="text-lg font-semibold tracking-tight">{title}</h3>
      {description && (
        <p className="mt-1.5 max-w-sm text-sm text-muted-foreground">{description}</p>
      )}
      {action && <div className="mt-6">{action}</div>}
    </div>
  );
}
