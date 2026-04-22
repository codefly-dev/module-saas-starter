# saas-starter — frontend architecture

**Pattern**: feature-sliced functional core. Not MVC (legacy term, maps poorly to React).

**Goal**: unit-testable business logic, dumb components, strict state taxonomy, fast feedback loops.

## Directory layout

```
src/
├── features/              # vertical slices, one per business domain
│   ├── users/
│   │   ├── core/          # pure TS — no React, no fetch, no browser APIs
│   │   │   ├── validation.ts
│   │   │   ├── permissions.ts
│   │   │   ├── transforms.ts
│   │   │   ├── types.ts
│   │   │   └── *.test.ts   # Vitest, no DOM, 1000+ tests/sec
│   │   ├── api/           # typed fetch wrappers, the only layer that knows about HTTP
│   │   │   ├── usersApi.ts
│   │   │   └── usersApi.test.ts  # MSW
│   │   ├── hooks/         # thin wrappers: TanStack Query + calls into core/
│   │   │   ├── useUsers.ts
│   │   │   └── useUsers.test.ts  # renderHook
│   │   ├── components/    # dumb React, receive props, render JSX
│   │   │   ├── UserTable.tsx
│   │   │   └── UserTable.stories.tsx  # Storybook
│   │   ├── containers/    # wire hooks → components, one per page
│   │   │   └── UsersPageContainer.tsx
│   │   └── index.ts       # barrel: only re-export the containers + hook types
│   ├── orgs/
│   ├── teams/
│   ├── invitations/
│   ├── audit/
│   └── entitlements/
├── app/                   # Next.js App Router, 5-line page files
│   ├── users/page.tsx     # import { UsersPageContainer } from '@/features/users'
│   └── ...
├── lib/                   # cross-feature primitives (auth, fetcher, error types)
│   ├── fetcher.ts         # global fetch wrapper, 401 handling, correlation ids
│   ├── auth/              # AuthKit SDK wiring, session cookie reads
│   └── ui/                # design system primitives (button, input, card)
└── test/
    ├── msw/               # mock HTTP handlers, re-used by any test that needs API stubs
    └── testUtils.tsx      # shared test providers (QueryClient, Router)
```

## The four rules

### Rule 1 — `core/` is pure TypeScript
No import of:
- `react` / `react-dom`
- `next/*`
- `@tanstack/react-query`
- `fetch` / `window` / `document` / `localStorage`
- Anything from `components/`, `hooks/`, `api/`, or `containers/` — `core/` depends on NOTHING

Only allowed imports: other `core/` modules (within the same feature), standard TS types, `zod`, `date-fns`, pure utility libs.

Enforced by ESLint: `.eslintrc` rule `no-restricted-imports` applied to `src/features/*/core/**`.

### Rule 2 — Dependency direction is one-way
```
containers → hooks → (api + core)
           → components → core
```
- `containers/` orchestrates. Imports hooks, components, and optionally core helpers.
- `hooks/` own state. Import core + api.
- `components/` are pure. Import core (for types only) and the design system.
- `api/` knows HTTP. Imports core types and `lib/fetcher`.
- `core/` has no siblings inside the feature.

A feature NEVER imports another feature's `core/` or `components/`. If shared logic is needed, it goes in `lib/`.

### Rule 3 — State taxonomy
Four kinds of state, four different tools:
- **Server state** → TanStack Query. Never `useState` for API data.
- **URL state** → Next.js router `useSearchParams()`. Sharable, bookmarkable.
- **Form state** → React Hook Form + Zod schema. Validation is a pure function in `core/`.
- **Client state** → `useState` for component-local, Zustand only for cross-component client state (rare — usually you don't need it).

### Rule 4 — View models
Components receive ready-to-render flat objects. Transformation happens in a pure `core/` function the hook calls.

```ts
// core/transforms.ts — pure, testable
export function toUserTableRows(users: User[], members: OrgMember[]): UserTableRow[] { ... }

// hooks/useUserTable.ts — owns state
export function useUserTable(orgId: UUID) {
  const users = useUsers(orgId);
  const members = useOrgMembers(orgId);
  return { rows: toUserTableRows(users.data ?? [], members.data ?? []), isLoading: users.isLoading };
}

// components/UserTable.tsx — dumb
export function UserTable({ rows }: { rows: UserTableRow[] }) { return <table>...</table>; }

// containers/UsersPageContainer.tsx — wiring
export function UsersPageContainer() {
  const { rows, isLoading } = useUserTable(useOrgId());
  if (isLoading) return <Spinner />;
  return <UserTable rows={rows} />;
}
```

## Testing strategy

### Unit (fast, many)
- Target: `core/` functions. 100% line coverage is realistic here.
- Tool: Vitest.
- No DOM, no RTL, no providers. Pure function tests.
- Runtime: sub-millisecond per test. A full `core/` test run completes in under a second.

### Hook tests (medium, some)
- Target: `hooks/` functions.
- Tool: Vitest + `@testing-library/react` `renderHook`.
- MSW for the API layer.
- Runtime: ~10ms per test.

### Component tests (medium, few)
- Target: `components/` interactive behaviour only. Click handlers, keyboard nav, a11y.
- Tool: RTL + Vitest.
- Do NOT test visual appearance — that's Storybook's job.
- Don't test data flow — that's the hook's job.

### Integration (slow, very few)
- Target: `containers/` + full page renders.
- Tool: RTL + MSW.
- One or two tests per container, covering the golden path.

### E2E (slowest, critical only)
- Target: the 5 critical user journeys (login, signup, invite, accept invite, impersonate).
- Tool: Playwright.
- Runs against real backend via `codefly run`.

### Storybook
- Every component in `components/` gets a story.
- Designers iterate without a backend or a running app.

## ESLint rules to enforce

```json
{
  "overrides": [
    {
      "files": ["src/features/*/core/**/*.{ts,tsx}"],
      "rules": {
        "no-restricted-imports": [
          "error",
          {
            "patterns": [
              { "group": ["react", "react-*"], "message": "core/ must be pure TS" },
              { "group": ["next", "next/*"], "message": "core/ must be pure TS" },
              { "group": ["@tanstack/*"], "message": "core/ must not know about server state" },
              { "group": ["../api/*", "../hooks/*", "../components/*", "../containers/*"],
                "message": "core/ is the root of the dependency graph" }
            ]
          }
        ]
      }
    },
    {
      "files": ["src/features/*/components/**/*.tsx"],
      "rules": {
        "no-restricted-imports": [
          "error",
          {
            "patterns": [
              { "group": ["@tanstack/react-query"], "message": "components must not fetch — use a hook" },
              { "group": ["../api/*"], "message": "components must not call api directly" }
            ]
          }
        ]
      }
    }
  ]
}
```

## Existing code migration path

The current frontend (`src/app/*/page.tsx` + `src/lib/hooks/use-*.ts` + `src/plugins/*`) is organized by Next convention. Migrate one feature at a time:

### Phase A — scaffolding (≤2 hours)
1. Create `src/features/` directory.
2. Add ESLint rules above.
3. Create `src/features/_template/` with the structure, empty files, and a one-paragraph README showing the pattern. Delete when first real feature lands.
4. Verify `pnpm lint` still passes.

### Phase B — reference feature: users (~1 day)
1. Move current `src/lib/hooks/use-users.ts` + `src/app/users/page.tsx` + user-related components into `src/features/users/`.
2. Extract pure logic (validation, permissions, row transforms) into `core/`.
3. Write `core/` tests. Aim for 20+ pure tests covering every branch of the logic.
4. Create `UserTable` component, dumb. Add Storybook story.
5. `src/app/users/page.tsx` becomes `export { UsersPageContainer as default } from '@/features/users'`.

### Phase C — batch migration (~1 day)
Repeat Phase B for: orgs, teams, invitations, audit, entitlements. One commit per feature.

### Phase D — cleanup (~2 hours)
1. Delete empty `src/lib/hooks/` (or shrink to cross-feature hooks only).
2. Delete `src/plugins/` if the plugin system can be rebuilt on top of features (evaluate).
3. Collapse any remaining category folders.

## What this is NOT

- **Not Redux.** TanStack Query + React Hook Form + Zustand if absolutely needed.
- **Not Clean Architecture ceremony.** No ports/adapters per feature. `core/` is the port, that's enough.
- **Not XState everywhere.** Only if a feature has a genuinely complex multi-step workflow.
- **Not "smart container vs dumb component" as separate files everywhere.** Only the page-level container is separate; small components can be inline.
- **Not MVC.** The term is misleading in React.

## Why this pattern

- Fast test feedback: `core/` runs in milliseconds, reflects 80% of business value.
- Mechanical enforcement: ESLint rules prevent regression; you can't accidentally fetch from a component.
- Feature-local reasoning: a new developer reads one `features/users/` folder and understands users. No jumping around category folders.
- Agent-native: `core/` functions are pure TS and can be exposed as MCP tools. Any "business rule" an agent asks about is testable AND callable.
- Incremental migration: existing features stay in place, new ones land in the new structure, old ones migrate one at a time.
