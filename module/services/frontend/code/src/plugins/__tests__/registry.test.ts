import { readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { discoveredPlugins } from "../registry.generated";

const pluginsDir = join(dirname(fileURLToPath(import.meta.url)), "..");

describe("plugin auto-discovery registry", () => {
  it("registers exactly one AdminPlugin per plugin file (registry is current)", () => {
    const pluginFiles = readdirSync(pluginsDir, { withFileTypes: true })
      .filter(
        (d) => d.isFile() && d.name.endsWith(".ts") && d.name !== "registry.generated.ts",
      )
      .map((d) => d.name);

    expect(discoveredPlugins.length).toBe(pluginFiles.length);
    // If this fails, run `npm run generate:plugins` (predev/prebuild/pretest do
    // this automatically) — a plugin file was added/removed without regenerating.
  });

  it("auto-discovered plugin names are unique", () => {
    const names = discoveredPlugins.map((p) => p.name);
    expect(new Set(names).size).toBe(names.length);
  });
});
