/**
 * @codefly-dev/ui — the shared, versioned Codefly SaaS frontend kit.
 *
 * The top-level entry carries the server-safe surface: the plugin host's
 * contribution composition and the tokens-as-data skin mechanism. The client
 * runtime and UI adapters ship from the `./plugin-host/runtime` and
 * `./plugin-host/ui` subpaths so a server module never pulls a `"use client"`
 * boundary in transitively.
 */
export * from "./plugin-host/index.js";
export * from "./skin/index.js";
