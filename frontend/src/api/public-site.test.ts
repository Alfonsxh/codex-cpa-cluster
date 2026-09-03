import { describe, expect, it } from "vitest";

import { emailDomainHint, emailPlaceholder } from "./public-site";

describe("public site email domains", () => {
  it("formats configured domains as visible email suffixes", () => {
    expect(emailDomainHint(["Example.com", "@staff.example.com", "example.com"])).toBe(
      "可用企业邮箱后缀：@example.com、@staff.example.com"
    );
    expect(emailPlaceholder(["Example.com", "staff.example.com"])).toBe("name@example.com");
  });

  it("keeps old or unavailable site configuration usable", () => {
    expect(emailDomainHint(undefined)).toBe("");
    expect(emailPlaceholder(undefined)).toBe("name@example.com");
  });
});
