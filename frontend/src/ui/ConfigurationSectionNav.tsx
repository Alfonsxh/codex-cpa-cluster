const configurationSections = [
  { to: "/configuration", index: "01", label: "运行配置", description: "路由、配额与账号参数" },
  { to: "/settings", index: "02", label: "通用设置", description: "品牌、身份与安全" },
  { to: "/notifications", index: "03", label: "通知设置", description: "企业微信与预警规则" }
] as const;

export function ConfigurationSectionNav() {
  const pathname = window.location.pathname;

  return (
    <nav className="configuration-section-nav" aria-label="配置中心页面">
      {configurationSections.map((section) => {
        const active = pathname.startsWith(`/admin${section.to}`) || pathname.startsWith(section.to);
        return (
          <a
            key={section.to}
            className={active ? "active" : ""}
            href={`/admin${section.to}`}
            aria-current={active ? "page" : undefined}
          >
            <span className="configuration-section-index" aria-hidden="true">{section.index}</span>
            <span className="configuration-section-copy">
              <strong>{section.label}</strong>
              <small>{section.description}</small>
            </span>
            <span className="configuration-section-arrow" aria-hidden="true">›</span>
          </a>
        );
      })}
    </nav>
  );
}
