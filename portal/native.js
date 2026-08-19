(() => {
  "use strict";

  const grid = document.querySelector("#native-grid");
  const addCard = document.querySelector("#add-account-link");
  const count = document.querySelector("#account-count");
  const error = document.querySelector("#native-error");

  const accountCard = (account, index) => {
    const card = document.createElement(account.management_url ? "a" : "article");
    card.className = "native-card";
    if (account.management_url) {
      card.href = account.management_url;
      card.rel = "noreferrer";
    }

    const top = document.createElement("div");
    top.className = "native-card-top";
    const number = document.createElement("span");
    number.className = "native-index";
    number.textContent = String(index + 1).padStart(2, "0");
    const badge = document.createElement("span");
    badge.className = "access public";
    badge.textContent = "业务 CPA";
    top.append(number, badge);

    const content = document.createElement("div");
    const kicker = document.createElement("p");
    kicker.className = "kicker";
    kicker.textContent = account.id.toUpperCase();
    const title = document.createElement("h2");
    title.textContent = account.id;
    const access = document.createElement("p");
    access.textContent = account.management_url
      ? "仅允许从部署主机访问"
      : "公网入口不开放原生管理端口";
    content.append(kicker, title, access);

    const meta = document.createElement("div");
    meta.className = "native-meta";
    const scope = document.createElement("span");
    scope.textContent = account.group_enabled ? "账号已启用" : "账号已停用";
    const open = document.createElement("b");
    open.textContent = account.management_url ? "打开 ↗" : "仅本机可访问";
    meta.append(scope, open);

    card.append(top, content, meta);
    return card;
  };

  fetch("/admin/api/native-accounts", {
    credentials: "same-origin",
    cache: "no-store",
    headers: { Accept: "application/json" }
  })
    .then((response) => {
      if (response.status === 401) {
        const loginError = new Error("请先登录管理中心");
        loginError.loginRequired = true;
        throw loginError;
      }
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      return response.json();
    })
    .then((payload) => {
      const accounts = Array.isArray(payload.accounts) ? payload.accounts : [];
      accounts.forEach((account, index) => grid.insertBefore(accountCard(account, index), addCard));
      count.textContent = `${accounts.length} 个业务 CPA`;
    })
    .catch((requestError) => {
      count.textContent = requestError.loginRequired ? "需要管理员登录" : "列表不可用";
      error.querySelector("strong").textContent = requestError.loginRequired
        ? "请先登录管理中心"
        : "业务 CPA 列表读取失败";
      error.querySelector("span").textContent = requestError.loginRequired
        ? "登录后返回本页，系统才会读取业务账号；公网不会返回原生端口。"
        : "请稍后刷新，或进入管理中心检查服务状态。";
      error.hidden = false;
    });
})();
