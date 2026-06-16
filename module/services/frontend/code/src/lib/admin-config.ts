import { buildAdminConfig } from "./admin-core";
import { discoveredPlugins } from "@/plugins/registry.generated";

/**
 * Compose all plugins into the admin dashboard configuration.
 *
 * Plugins are AUTO-DISCOVERED: create a plugin file in src/plugins/ that exports
 * an AdminPlugin and it's registered automatically (the registry barrel is
 * regenerated on predev/prebuild/pretest). Side-modules — e.g. the warden
 * console — compose in with ZERO edits to this base file.
 */
export const adminConfig = buildAdminConfig(discoveredPlugins);
