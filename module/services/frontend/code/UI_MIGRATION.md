# Shared UI migration

`ui-inventory.json` records the merged baseline by source family, its imports,
owner, disposition and story coverage. An empty story list is a coverage gap,
not an exclusion. The merged source contains no explorer stories; previously
reported local previews are not release evidence. Package names in current
manifests use `@codefly-dev/saas-ui`.

## API decisions

- Preserve the host compound Card API as `CardRoot` with CardHeader, CardTitle,
  CardDescription, CardAction, CardContent and CardFooter. The existing data-in
  `Card` composes that root. Host compatibility files alias CardRoot to Card.
  This avoids an ambiguous union of DOM title attributes and ReactNode titles.
- Preserve compound Tabs as `TabsRoot`, TabsList, TabsTrigger and TabsContent.
  The data-in `Tabs` composes the same Base UI implementation. Its active/initial
  selection, invalid-selection recovery and keepMounted contract remain intact.
  Compound APIs use value/defaultValue/onValueChange and Base UI render props.
  Forward orientation to the underlying root, not only to a data attribute.
- Move Command, InputGroup and Sheet without changing their public composition.
  Their dependencies belong to the package that implements them.
- Sidebar presentation belongs to the kit. Responsive detection is reusable;
  cookie persistence remains a host callback. Toaster accepts theme from its
  host adapter. Neither package reads authentication or application contexts.
- Existing compatibility import paths remain thin re-exports for host source
  stability. New consumers use package exports; no private aliases are required.
  Remove these aliases only in a separately announced host source migration.
- Keep the existing CSS authority and token vocabulary. No package adds global
  CSS. Native refs and DOM events follow React 19; Base UI controls retain their
  own typed event details and focus behavior.

## Completion evidence

The inventory is an accounting tool, not a claim of completion. Missing stories,
provider-dependent previews, skin/browser checks, packed consumption and live
SaaS smoke checks must be recorded before declaring catalog coverage complete.
