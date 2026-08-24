import { apiRequest } from "./client";
import type { NativeAccountCatalog, PublicSiteConfiguration } from "./generated";

export type { NativeAccount, NativeAccountCatalog, PublicSiteConfiguration } from "./generated";

export const publicSiteQueryKey = ["public-site-configuration"] as const;
export const nativeAccountsQueryKey = ["native-accounts"] as const;

export const defaultPublicSiteConfiguration: PublicSiteConfiguration = {
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
    content_type: "image/svg+xml",
    sha256: "",
    updated_at: null
  }
};

export function readPublicSiteConfiguration(signal?: AbortSignal): Promise<PublicSiteConfiguration> {
  return apiRequest<PublicSiteConfiguration>("/site-config.json", { signal, cache: "no-store" });
}

export function listNativeAccounts(signal?: AbortSignal): Promise<NativeAccountCatalog> {
  return apiRequest<NativeAccountCatalog>("/admin/api/native-accounts", { signal, cache: "no-store" });
}
