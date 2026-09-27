// Package redisintegration holds the accounts tests that need a real Redis: the
// `cache` dependency, booted by codefly beside the real store. It has no
// non-test code. Its tests prove the membership cache stack across two service
// instances (pkg/membership) and the authoritative Redis state
// (pkg/redisstate) against the server the codefly.dev/redis agent runs.
package redisintegration
