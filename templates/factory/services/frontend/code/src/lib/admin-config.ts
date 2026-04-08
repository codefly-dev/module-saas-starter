import { buildAdminConfig } from "./admin-core";
import { coreUsersPlugin } from "@/plugins/core-users";
import { platformAdminPlugin } from "@/plugins/platform-admin";
import { auditPlugin } from "@/plugins/audit";

/**
 * Compose all plugins into the admin dashboard configuration.
 *
 * To add a new feature (e.g. billing, RAG), create a plugin file
 * in src/plugins/ and add it to this array.
 */
export const adminConfig = buildAdminConfig([
  coreUsersPlugin,
  platformAdminPlugin,
  auditPlugin,
]);
