(function () {
  "use strict";

  const defaults = Object.freeze({
    version: 1,
    product_name: "Codex CPA Cluster",
    short_name: "Codex CPA",
    environment_label: "Self-hosted service",
    public_base_url: "",
    provider_name: "Codex CPA",
    api_key_env: "CPA_API_KEY",
    default_model: "gpt-5.6-sol",
    logo: {
      custom: false,
      url: "/portal/assets/codex-cpa-cluster-logo.svg",
      sha256: "",
    },
  });

  const DEFAULT_LOGO_URL = defaults.logo.url;
  const DEFAULT_LOGO_DARK_URL = "/portal/assets/codex-cpa-cluster-logo-dark.svg";

  const currentTheme = () => document.documentElement.dataset.theme === "dark" ? "dark" : "light";

  const defaultLogoUrl = () => currentTheme() === "dark" ? DEFAULT_LOGO_DARK_URL : DEFAULT_LOGO_URL;

  const renderLogos = (config) => {
    const custom = Boolean(config.logo?.custom);
    const logoUrl = custom ? (config.logo?.url || DEFAULT_LOGO_URL) : defaultLogoUrl();
    const version = custom && config.logo?.sha256
      ? `?v=${encodeURIComponent(config.logo.sha256.slice(0, 16))}`
      : "";
    document.querySelectorAll("[data-brand-logo]").forEach((node) => {
      node.src = `${logoUrl}${version}`;
      node.alt = config.product_name;
      node.dataset.brandLogoSource = custom ? "custom" : "default";
    });
  };

  const normalize = (payload) => ({
    ...defaults,
    ...(payload && typeof payload === "object" ? payload : {}),
    logo: { ...defaults.logo, ...(payload?.logo || {}) },
  });

  const apply = (config) => {
    const page = document.documentElement.dataset.brandPage || "";
    document.title = page ? `${page} · ${config.product_name}` : config.product_name;
    document.querySelectorAll("[data-brand-product-name]").forEach((node) => {
      node.textContent = config.product_name;
    });
    document.querySelectorAll("[data-brand-short-name]").forEach((node) => {
      node.textContent = config.short_name;
    });
    document.querySelectorAll("[data-brand-environment]").forEach((node) => {
      node.textContent = config.environment_label || "Self-hosted service";
    });
    renderLogos(config);
    document.querySelectorAll("[data-brand-email-placeholder]").forEach((node) => {
      node.placeholder = "name@example.com";
    });
    window.dispatchEvent(new CustomEvent("cpa-branding-ready", { detail: config }));
    return config;
  };

  document.addEventListener("cpa-theme-change", () => {
    if (window.cpaBrandingConfig) renderLogos(window.cpaBrandingConfig);
  });

  window.cpaBrandingReady = fetch("/site-config.json", {
    credentials: "same-origin",
    cache: "no-store",
    headers: { Accept: "application/json" },
  })
    .then((response) => response.ok ? response.json() : Promise.reject(new Error(`HTTP ${response.status}`)))
    .then(normalize)
    .catch(() => normalize(defaults))
    .then((config) => {
      window.cpaBrandingConfig = config;
      return apply(config);
    });
}());
