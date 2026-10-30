import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";
import { openModelSettingsSection } from "./model-settings-helper";

// Real components and loopback HTTP only; no account, SQL or provider.
async function fixture(
  page: Page,
  info: TestInfo,
  mode: "initial" | "history",
) {
  const component = "/src/features/models/ModelsPage.vue";
  const source = await (await page.request.get(component)).text();
  const vue = source.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  const counts = { reads: 0, hangs: 0, aborted: 0, writes: 0, writeClosed: 0 };
  let modelsFailure: 403 | 503 | undefined;
  let historyReads = 0;
  const settings = {
    catalog: [
      {
        id: "deepseek",
        name: "DeepSeek",
        model: "test-only",
        base_url: "https://example.com",
        source: "fixture",
        checked_at: "2026-10-28",
      },
    ],
    connections: [],
    bindings: [],
    roles: ["writer"],
    paid_enabled: false,
  };
  const budget = {
    currency_locked: false,
    currency: "USD",
    limit_micros: 10000000,
    per_call_micros: 1000000,
    spent_micros: 0,
    reserved_micros: 1000000,
    enabled: false,
    revision: 1,
  };
  const call = {
    id: "call-1",
    state: "UNKNOWN",
    project_name: "隔离未知请求",
    provider: "fixture",
    model: "fixture-only",
    currency: "USD",
    revision: 1,
    charged_micros: 0,
    reserved_micros: 1000000,
    provider_receipt: "",
    receipt_count: 0,
    can_reconcile: true,
  };
  const server = createServer((request, response) => {
    const path = new URL(request.url!, "http://127.0.0.1").pathname;
    const json = (value: unknown) =>
      response
        .writeHead(200, { "Content-Type": "application/json" })
        .end(JSON.stringify(value));
    if (request.method !== "GET") {
      counts.writes++;
      response.on("close", () => counts.writeClosed++);
      request.resume();
      return; // Unknown write outcome: never synthesize success or retry.
    }
    if (path === "/") {
      response.writeHead(200, { "Content-Type": "text/html; charset=utf-8" })
        .end(`<!doctype html><meta charset="utf-8"><button id="leave">外部离开</button><div id="app"></div><script type="module">
import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};
import ModelsPage from ${JSON.stringify(vite + component)};
window.expiredEvents=0;
const app=createApp({render:()=>h(ModelsPage,{onExpired:()=>window.expiredEvents++})});app.mount('#app');
document.querySelector('#leave').onclick=()=>app.unmount();</script>`);
      return;
    }
    counts.reads++;
    if (
      (mode === "initial" && counts.reads <= 3) ||
      (path.endsWith("/history") && historyReads++ === 0)
    ) {
      counts.hangs++;
      response.on("close", () => counts.aborted++);
      return;
    }
    if (path === "/api/models") {
      if (modelsFailure) {
        const code = modelsFailure;
        modelsFailure = undefined;
        response
          .writeHead(code, { "Content-Type": "application/json" })
          .end('{"error":"读取失败"}');
        return;
      }
      json(settings);
    } else if (path === "/api/model-budget") json(budget);
    else if (path === "/api/model-calls")
      json({ items: mode === "history" ? [call] : [], next_cursor: "" });
    else if (path.endsWith("/history")) json({ adjustments: [], receipts: [] });
    else response.writeHead(404).end();
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const now = new Date("2026-10-28T00:00:00Z");
  await page.clock.install({ time: now });
  await page.clock.pauseAt(now);
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  return {
    counts,
    failModels: (status: 403 | 503) => {
      modelsFailure = status;
    },
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

test("设置首屏挂起后释放返回并可重试，刷新保留草稿且权限拒绝不沿用配置", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "initial");
  try {
    await expect.poll(() => state.counts.hangs).toBe(3);
    await expect(
      page.getByRole("button", { name: "返回项目", exact: true }),
    ).toBeDisabled();
    await page.clock.fastForward(15000);
    await expect.poll(() => state.counts.aborted).toBe(3);
    await expect(
      page.getByRole("button", { name: "返回项目", exact: true }),
    ).toBeEnabled();
    await page
      .getByRole("button", { name: "刷新模型配置", exact: true })
      .click();
    await openModelSettingsSection(page, "预算与账单");
    await page
      .getByRole("button", { name: "重新加载预算", exact: true })
      .click();
    await page
      .getByRole("button", { name: "刷新请求账目", exact: true })
      .click();
    await openModelSettingsSection(page, "模型与 Key");
    await page
      .getByRole("button", { name: "添加 DeepSeek", exact: true })
      .click();
    await page.getByLabel("配置名称", { exact: true }).fill("作者未保存配置");
    await page
      .getByLabel("API Key", { exact: true })
      .fill("test-only-unsaved-key");
    await openModelSettingsSection(page, "预算与账单");
    await page.getByLabel("累计预算总额", { exact: true }).fill("25");
    state.failModels(503);
    await page
      .getByRole("button", { name: "刷新模型配置", exact: true })
      .click();
    await expect(
      page.getByRole("alert").filter({ hasText: "读取失败" }),
    ).toBeVisible();
    await expect(page.getByLabel("API Key", { exact: true })).toHaveValue(
      "test-only-unsaved-key",
    );
    await page
      .getByRole("button", { name: "刷新费用统计", exact: true })
      .click();
    await expect(page.getByLabel("累计预算总额", { exact: true })).toHaveValue(
      "25",
    );
    state.failModels(403);
    await page
      .getByRole("button", { name: "刷新模型配置", exact: true })
      .click();
    await expect(page.getByLabel("API Key", { exact: true })).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "添加 DeepSeek", exact: true }),
    ).toHaveCount(0);
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
});

test("离开设置页取消所有只读请求，迟到错误不发送会话事件", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "initial");
  try {
    await expect.poll(() => state.counts.hangs).toBe(3);
    await page.locator("#leave").click();
    await expect.poll(() => state.counts.aborted).toBe(3);
    await page.clock.fastForward(20000);
    expect(
      await page.evaluate(
        () => (window as unknown as { expiredEvents: number }).expiredEvents,
      ),
    ).toBe(0);
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
});

test("账单历史读取超时可恢复，保存结果未知不按GET时限取消或重发", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "history");
  try {
    await openModelSettingsSection(page, "预算与账单");
    const panel = page.locator(".reconciliation-panel");
    await panel.getByRole("button", { name: /隔离未知请求/ }).click();
    await expect.poll(() => state.counts.hangs).toBe(1);
    await page.clock.fastForward(15000);
    await expect.poll(() => state.counts.aborted).toBe(1);
    await expect(
      page.getByRole("button", { name: "返回项目", exact: true }),
    ).toBeEnabled();
    await panel.getByRole("button", { name: /隔离未知请求/ }).click();
    await panel.getByLabel("账单累计金额", { exact: true }).fill("0.5");
    await panel.getByLabel("账单或回执编号").fill("fixture-bill");
    await panel
      .getByLabel("核对依据")
      .fill("隔离合同测试，不是真实账单或模型调用");
    await panel.getByLabel("本次调整原因").fill("仅验证未知写请求边界");
    await panel.getByLabel("我已核对账单与此请求的对应关系").check();
    await panel
      .getByRole("button", { name: "确认保存账单记录", exact: true })
      .click();
    await expect.poll(() => state.counts.writes).toBe(1);
    await page.clock.fastForward(30000);
    expect(state.counts.writes).toBe(1);
    expect(state.counts.writeClosed).toBe(0);
    await expect(
      panel.getByRole("button", { name: "确认保存账单记录", exact: true }),
    ).toBeDisabled();
    await expect(
      page.getByRole("button", { name: "返回项目", exact: true }),
    ).toBeDisabled();
  } finally {
    await state.close();
  }
});
