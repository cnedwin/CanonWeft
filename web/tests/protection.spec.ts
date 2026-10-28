import { loginAsOwner } from "./owner-session";
import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";

test("自动备份状态可见，查看保护不会丢掉未保存的项目名称", async ({ page }) => {
  await page.clock.install();
  await loginAsOwner(page);
  await page.getByLabel("项目名称", { exact: true }).fill("尚未保存的小说构思");
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "作品的备份保护" }),
  ).toBeVisible();
  await expect(page.getByText("独立介质恢复尚未验收。")).toBeVisible();
  await expect
    .poll(async () => {
      const response = await page.request.get("/api/operations/protection");
      expect(response.status()).toBe(200);
      const status = await response.json();
      expect(typeof status.log_recovery).toBe("string");
      return status.checkpoint_state;
    })
    .toBe("CURRENT");
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await expect(
    page.getByText("已有最近的恢复点", { exact: true }),
  ).toBeVisible();
  const held: import("@playwright/test").Route[] = [];
  await page.route("**/api/operations/protection", (route) => {
    held.push(route);
  });
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await expect.poll(() => held.length).toBeGreaterThan(0);
  await page.clock.fastForward(16 * 60 * 1000);
  await expect(
    page.getByText("恢复点已超过 15 分钟，请检查保护状态", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("已有最近的恢复点", { exact: true }),
  ).not.toBeVisible();
  for (const route of held) await route.abort();
  await expect(
    page.getByText("暂时无法确认保护状态", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "返回工作室", exact: true }).click();
  await expect(page.getByLabel("项目名称", { exact: true })).toHaveValue(
    "尚未保存的小说构思",
  );
});

test("日志缺口不会被新恢复点隐藏，等待中的状态会按数据时间变旧", async ({
  page,
}) => {
  await page.clock.install();
  await loginAsOwner(page);
  const now = new Date().toISOString();
  await page.route("**/api/operations/protection", (route) =>
    route.fulfill({
      json: {
        checkpoint_state: "CURRENT",
        attempt_state: "CURRENT",
        checked_at: now,
        running: false,
        retention_days: 35,
        latest: {
          id: "test-point",
          snapshot_at: now,
          kind: "baseline",
          bytes: 100,
        },
        log_coverage: {
          state: "CURRENT_WITH_GAPS",
          checked_at: now,
          coverage_from: now,
          latest: { observed_at: now },
          running: false,
          history_gaps: 1,
          cleanup_state: "NEEDS_ATTENTION",
        },
      },
    }),
  );
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  await expect(
    page.getByText("新的日志保护正常，历史中存在缺口", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(
      "历史缺口不会因建立新备份而消失。恢复时只能选择已验证的完整区间或快照。",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(page.getByText(/部分过期文件清理失败/)).toBeVisible();
  await page.unroute("**/api/operations/protection");
  const held: import("@playwright/test").Route[] = [];
  await page.route("**/api/operations/protection", (route) => {
    held.push(route);
  });
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await expect.poll(() => held.length).toBeGreaterThan(0);
  await page.clock.fastForward(16 * 60 * 1000);
  await expect(
    page.getByText("日志恢复点已超过 15 分钟，请检查保护状态", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("新的日志保护正常，历史中存在缺口", { exact: true }),
  ).not.toBeVisible();
  for (const route of held) await route.abort();
  await expect(
    page.getByText("暂时无法确认连续日志状态", { exact: true }),
  ).toBeVisible();
});

test("真实自动日志采集状态显示来源覆盖时间", async ({ page }) => {
  await loginAsOwner(page);
  await expect(
    page.getByRole("button", { name: "备份保护", exact: true }),
  ).toBeVisible();
  await expect
    .poll(
      async () => {
        const response = await page.request.get("/api/operations/protection");
        expect(response.status()).toBe(200);
        const body = await response.json();
        const logs = body.log_coverage;
        if (!logs) return false;
        return (
          ["CURRENT", "CURRENT_WITH_GAPS"].includes(logs.state) &&
          Date.now() - Date.parse(logs.latest.observed_at) < 15 * 60 * 1000
        );
      },
      { timeout: 90_000 },
    )
    .toBe(true);
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "连续日志保护", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("日志已覆盖到", { exact: true })).toBeVisible();
  await expect(
    page.getByText("连续日志保护尚未配置", { exact: true }),
  ).not.toBeVisible();
});

test("诊断导出只有运行元数据，不包含创作内容与凭据字段", async ({
  page,
  request,
}) => {
  expect((await request.get("/api/operations/diagnostics")).status()).toBe(401);
  await loginAsOwner(page);
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "导出诊断信息", exact: true }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("studio-diagnostics.json");
  const path = await download.path();
  expect(path).not.toBeNull();
  const body = await readFile(path!, "utf8");
  const report = JSON.parse(body);
  expect(report.operations.format).toBe(1);
  expect(report.operations.schema_version).toBeGreaterThanOrEqual(31);
  expect(report.operations.states.model_calls).toBeDefined();
  expect(report.protection.checkpoint_state).toBeDefined();
  for (const field of [
    "password_hash",
    "credential_ref",
    "input_hash",
    "frozen",
    "prompt",
    "username",
  ])
    expect(body).not.toContain(`"${field}"`);
});
