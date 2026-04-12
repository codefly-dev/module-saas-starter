# Feature template

Every feature under `src/features/` follows this shape. It's **feature-sliced
functional core**, not MVC — the name "MVC" maps badly to React.

See `/FRONTEND_ARCHITECTURE.md` at the module root for the full rules.

## Layers

```
src/features/<name>/
├── core/          # PURE TypeScript. No React, no fetch, no browser APIs.
│   ├── types.ts         # domain types
│   ├── validation.ts    # Zod schemas + validators
│   ├── permissions.ts   # canDoX(user, resource) booleans
│   ├── transforms.ts    # data → view-model shapers
│   └── *.test.ts        # Vitest, no DOM
├── api/           # Typed fetch wrappers. Only layer that knows HTTP.
│   ├── <name>Api.ts
│   └── <name>Api.test.ts   # MSW
├── hooks/         # Thin wrappers: TanStack Query + calls into core/
│   ├── use<Name>.ts
│   └── use<Name>.test.ts   # renderHook
├── components/    # Dumb React. Receive props, render JSX. No fetches.
│   ├── <Name>Table.tsx
│   └── <Name>Table.stories.tsx   # Storybook
├── containers/    # Wire hooks → components. One per page.
│   └── <Name>PageContainer.tsx
└── index.ts       # barrel: re-export containers + hook types only
```

## The four rules

1. **`core/` is pure TypeScript.** No `react`, `next`, `@tanstack/*`, no
   `fetch`, no `window`, no `localStorage`. Enforced by ESLint.
2. **Dependency direction is one-way:**
   `containers → hooks → (api + core) | components → core`
3. **State taxonomy:** server → TanStack Query. URL → Next router.
   Form → React Hook Form + Zod. Client → `useState` / Zustand if needed.
4. **View models:** components receive ready-to-render flat objects from a
   pure `core/` shaper. No transformation in JSX.

## Testing

- `core/` → Vitest, 100% coverage target, sub-millisecond tests
- `hooks/` → `renderHook` + MSW
- `components/` → RTL for interactive behavior only; Storybook for visual
- `containers/` → one integration test per container
- E2E (Playwright) → the 5 critical user journeys only

## Example

```ts
// core/transforms.ts — pure, tested in isolation
import type { User, OrgMember } from "./types";
import type { UserTableRow } from "./types";

export function toUserTableRows(users: User[], members: OrgMember[]): UserTableRow[] {
  return users.map((u) => ({
    id: u.id,
    email: u.email,
    role: members.find((m) => m.userId === u.id)?.role ?? "—",
  }));
}

// hooks/useUserTable.ts — owns state, calls core
export function useUserTable(orgId: string) {
  const users = useUsers(orgId);
  const members = useOrgMembers(orgId);
  return {
    rows: toUserTableRows(users.data ?? [], members.data ?? []),
    isLoading: users.isLoading || members.isLoading,
  };
}

// components/UserTable.tsx — dumb
export function UserTable({ rows }: { rows: UserTableRow[] }) {
  return <table>{/* render rows */}</table>;
}

// containers/UsersPageContainer.tsx — wiring
export function UsersPageContainer() {
  const { rows, isLoading } = useUserTable(useOrgId());
  if (isLoading) return <Spinner />;
  return <UserTable rows={rows} />;
}
```

## When migrating an existing feature

1. Create the directory.
2. Copy existing logic into `core/` as pure functions.
3. Write unit tests for every branch of `core/` before moving components.
4. Move hooks, wiring them through the new `core/` helpers.
5. Move components, stripping any data fetching.
6. Update `src/app/<name>/page.tsx` to import the container.

Delete this `_template/` directory once at least one real feature has been
migrated.
