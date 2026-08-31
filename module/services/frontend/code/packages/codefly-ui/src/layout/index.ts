// The shared layout kit: pure, data-in page primitives (Tabs, Card, Section) so a
// solution's Page.tsx composes a real page from one shared package instance rather
// than raw HTML or host-internal `@/shared/ui`. Exported from `@codefly/ui/layout`,
// mirroring `@codefly/ui/dashboard`. No host context, no SDK — React only.

export { Card, type CardProps, Section, type SectionProps } from "./card.js";
export { type TabItem, Tabs, type TabsProps } from "./tabs.js";
