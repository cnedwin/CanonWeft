import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";
import { openModelSettingsSection } from "./model-settings-helper";

async function fixture(page: Page, info: TestInfo) {
  const component = "/src/features/models/ModelsPage.vue";
  const source = await (await page.request.get(component)).text();
  const vue = source.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  let writes = 0;
  let denied = false;
  const settings: Record<string, unknown> = {
    catalog: [
      {
        id: "deepseek",
        name: "DeepSeek",
        model: "fixture-only",
        base_url: "https://api.deepseek.com",
        source: "fixture",
        checked_at: "2026-10-29",
      },
    ],
    connections: [
      {
        id: "local",
        name: "本地已存模型",
        model: "fixture-only",
        enabled: true,
        has_key: true,
        revision: 2,
      },
    ],
    bindings: [
      {
        role: "writer",
        connection_id: "local",
        connection_revision: 1,
        revision: 1,
      },
    ],
    roles: [
      "architect",
      "planner",
      "writer",
      "bookkeeper",
      "critic",
      "image_generator",
    ],
    paid_enabled: false,
    setup: {
      available: true,
      checked_at: "2026-10-29T00:00:00Z",
      network_disabled: true,
      budget: {
        currency: "USD",
        enabled: false,
        configured: true,
        available_micros: 4000000,
        per_call_micros: 1000000,
      },
      roles: [
        "architect",
        "planner",
        "writer",
        "bookkeeper",
        "critic",
        "image_generator",
      ].map((role) => ({ role, code: "BINDING_MISSING" })),
    },
  };
  const server = createServer((req, res) => {
    const path = new URL(req.url!, "http://127.0.0.1").pathname;
    const json = (value: unknown) =>
      res
        .writeHead(200, { "Content-Type": "application/json" })
        .end(JSON.stringify(value));
    if (req.method !== "GET") {
      writes++;
      req.resume();
      res.writeHead(405).end();
      return;
    }
    if (path === "/") {
      res.writeHead(200, { "Content-Type": "text/html;charset=utf-8" })
        .end(`<!doctype html><meta charset="utf-8"><div id="app"></div><script type="module">
import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};
import ModelsPage from ${JSON.stringify(vite + component)};
createApp({render:()=>h(ModelsPage)}).mount('#app');</script>`);
    } else if (path === "/api/models") {
      if (denied)
        res
          .writeHead(403, { "Content-Type": "application/json" })
          .end('{"error":"权限不足"}');
      else json(settings);
    } else if (path === "/api/model-budget")
      json({
        currency: "USD",
        enabled: false,
        revision: 1,
        limit_micros: 5000000,
        per_call_micros: 1000000,
        spent_micros: 0,
        reserved_micros: 1000000,
      });
    else if (path === "/api/model-calls") json({ items: [], next_cursor: "" });
    else res.writeHead(404).end();
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  return {
    settings,
    writes: () => writes,
    deny: () => {
      denied = true;
    },
    allow: () => {
      denied = false;
    },
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

test("开写配置清单解释缺口、保留固定版本与草稿，定位不提交或启用费用", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    await openModelSettingsSection(page, "开写检查");
    const panel = page.getByRole("region", { name: "开写前配置检查" });
    await expect(panel).toBeVisible();
    await expect(panel).toContainText("模型联网已关闭");
    await expect(panel).toContainText("付费调用已关闭");
    await expect(panel).toContainText("4 USD");
    await panel
      .getByRole("button", { name: "正文创作：去设置", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "专业用途绑定", exact: true }),
    ).toBeInViewport();
    await openModelSettingsSection(page, "模型与 Key");
    await page
      .getByRole("button", { name: "添加 DeepSeek", exact: true })
      .click();
    await page.getByLabel("配置名称", { exact: true }).fill("未保存的作者选择");
    const report = state.settings.setup as { roles: unknown[] };
    report.roles = [
      {
        role: "writer",
        code: "CONFIGURED",
        connection_id: "local",
        bound_revision: 1,
        current_revision: 2,
        expires_at: "2026-10-30T00:00:00Z",
      },
    ];
    await page
      .getByRole("button", { name: "刷新模型配置", exact: true })
      .click();
    await openModelSettingsSection(page, "开写检查");
    await expect(panel).toContainText("配置项完整");
    await expect(panel).toContainText("固定版本 1");
    await expect(panel).toContainText("当前版本 2");
    await expect(page.getByLabel("配置名称", { exact: true })).toHaveValue(
      "未保存的作者选择",
    );
    await expect(page.getByText("需要重新绑定新版")).toHaveCount(0);
    expect(state.writes()).toBe(0);
    (state.settings.setup as { available: boolean }).available = false;
    await page
      .getByRole("button", { name: "刷新模型配置", exact: true })
      .click();
    await expect(panel).toContainText("配置检查暂不可用");
    await expect(panel).not.toContainText("配置项完整");
    await expect(page.getByLabel("配置名称", { exact: true })).toHaveValue(
      "未保存的作者选择",
    );
    state.deny();
    await page
      .getByRole("button", { name: "刷新模型配置", exact: true })
      .click();
    await expect(panel).toHaveCount(0);
    state.allow();
    delete state.settings.setup;
    await page
      .getByRole("button", { name: "刷新模型配置", exact: true })
      .click();
    await openModelSettingsSection(page, "模型与 Key");
    await expect(
      page.getByRole("button", { name: "添加 DeepSeek", exact: true }),
    ).toBeVisible();
    await expect(panel).toHaveCount(0); // Older server responses stay compatible.
    expect(state.writes()).toBe(0);
  } finally {
    await state.close();
  }
});
