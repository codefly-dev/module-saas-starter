// Next.js accepts hostnames, not URL origins, for its development allowlist.
// Only explicit operator configuration is trusted; never reflect request headers.
export function developmentOrigins(value = "") {
  const hosts = new Set();
  for (const entry of value.split(/[\s,]+/).filter(Boolean)) {
    let url;
    try {
      url = new URL(entry.includes("://") ? entry : `https://${entry}`);
    } catch {
      throw new Error("FRONTEND_ALLOWED_DEV_ORIGINS contains an invalid origin");
    }
    if (
      !["http:", "https:"].includes(url.protocol) ||
      !url.hostname || url.hostname.includes("*") ||
      url.username || url.password || url.pathname !== "/" ||
      url.search || url.hash
    ) {
      throw new Error("FRONTEND_ALLOWED_DEV_ORIGINS requires explicit HTTP(S) origins or hostnames");
    }
    hosts.add(url.hostname);
  }
  return [...hosts];
}
