# Example skins

Two complete, valid **skin descriptors** that demonstrate what the runtime SSR
skin resolver (`src/lib/skin/`) can do. A skin is *data*, never CSS or code — it
is validated through the plugin contract's `resolveFrontendAppearance` (the
single injection gate) and a branding asset allowlist before it renders.

These files are exactly the payloads that a deployment mounts as a ConfigMap
(one per environment/tenant) in `obin-fleet`. Here they live as fixtures so the
capability is visible and testable in-repo.

| | **Helios** | **Nocturne** |
|---|---|---|
| Default theme | light | dark |
| Corners (`radius`) | `1rem` (rounded) | `0` (sharp) |
| Body font | Trebuchet MS / Segoe UI | Helvetica Neue / Arial |
| Heading font | Georgia serif | Courier New mono |
| Density (`spacing`) | `0.28rem` (airy) | `0.22rem` (dense) |
| Sidebar width | `17rem` | `15rem` |
| Shadow strength | `1.4` | `0.4` |
| Primary hue | warm orange | violet |
| Accent | lime | cyan |
| Wordmark | `/brand/helios-logo.svg` | `/brand/nocturne-logo.svg` |

Logo assets live in [`public/brand/`](../../public/brand) (light + dark
variants) and are referenced with root-relative paths, which the resolver's
asset allowlist permits (https URLs are also allowed; `data:`/`http:` are not).

## What a skin can set

- **Appearance** — `defaultTheme`, `radius`, `fontSans`, `fontHeading`, the
  structural tokens (`spacing`, `fontSizeBase`, `sidebarWidth`,
  `sidebarWidthIcon`, `borderWidth`, `shadowStrength`), and per-mode `light` /
  `dark` color tokens (partial: any omitted token inherits the compiled
  default).
- **Branding** — `name`, `mark`, `title`, `description`, `favicon`, and a
  `logo` (`lightSrc` / `darkSrc` / `alt`).

Anything unknown, out of range, or unsafe (e.g. raw CSS, a `data:` logo) is
rejected and the page falls back to the compiled default skin — it can never
break a render.

## Run one locally

The skinnable image reads request headers at SSR, which requires dynamic
rendering. Build with the runtime flag, then point the file source at one skin
directory (each dir's `default.json` is the fallback skin for that deployment;
`<host>.json` can override per host):

```bash
# build a skinnable image (forces dynamic rendering)
FRONTEND_SKIN_RUNTIME=1 npm run build

# resolve the Helios skin from a mounted directory
FRONTEND_SKIN_DIR="$(pwd)/examples/skins/helios" npm start
# ...or Nocturne
FRONTEND_SKIN_DIR="$(pwd)/examples/skins/nocturne" npm start
```

Or inline a single skin without a directory:

```bash
FRONTEND_SKIN_JSON="$(cat examples/skins/nocturne/default.json)" npm start
```

Omit all `FRONTEND_SKIN_*` variables and the app renders the compiled default
("Launchpad") — no runtime cost, static rendering preserved.
