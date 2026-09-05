type Application = "admin" | "usage" | "portal";

const applicationPaths = { admin: "/admin/", usage: "/usage/", portal: "/" } as const;

// Only cross-application links belong here. API URLs and React Router links
// within one application must keep their existing same-origin paths.
export function applicationHref(application: Application, search = ""): string {
  const href = `${applicationPaths[application]}${search}`;
  if (!import.meta.env.DEV) return href;

  const { origin: configuredOrigin, port, originVariable } = {
    admin: { origin: import.meta.env.VITE_DEV_ADMIN_ORIGIN, port: 5173, originVariable: "VITE_DEV_ADMIN_ORIGIN" },
    usage: { origin: import.meta.env.VITE_DEV_USAGE_ORIGIN, port: 5174, originVariable: "VITE_DEV_USAGE_ORIGIN" },
    portal: { origin: import.meta.env.VITE_DEV_PORTAL_ORIGIN, port: 5175, originVariable: "VITE_DEV_PORTAL_ORIGIN" }
  }[application];
  // Keep localhost navigation on localhost: cookies are shared across ports,
  // but not between localhost and 127.0.0.1. Other hosts require an override.
  const hostname = typeof window !== "undefined" && window.location.hostname === "localhost"
    ? "localhost"
    : "127.0.0.1";
  let origin: URL;
  try {
    origin = new URL(configuredOrigin?.trim() || `http://${hostname}:${port}`);
    if (!["http:", "https:"].includes(origin.protocol) || origin.username || origin.password
      || origin.pathname !== "/" || origin.search || origin.hash) throw new Error();
  } catch {
    throw new Error(`${originVariable} must be an HTTP(S) origin without credentials, a path, query or fragment`);
  }
  return `${origin.origin}${href}`;
}
