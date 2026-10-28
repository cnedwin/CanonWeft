import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { resolve } from "node:path";

test("未知请求可凭账单解除占用并保留修正历史", async ({ page }) => {
  const fixture = JSON.parse(
    execFileSync(
      "go",
      ["run", "-buildvcs=false", "./tests/fixtures/reconciliation"],
      {
        cwd: resolve(process.cwd(), "../backend"),
        encoding: "utf8",
        windowsHide: true,
      },
    ),
  ) as { id: string; project: string; currency: string };
  await loginAsOwner(page);
  await page.getByRole("button", { name: "模型与用途设置" }).click();
  const panel = page.locator(".reconciliation-panel");
  await panel
    .getByRole("button", { name: new RegExp(fixture.project) })
    .click();
  await panel.getByLabel("账单累计金额", { exact: true }).fill("0.000045");
  await panel.getByLabel("账单或回执编号").fill("测试账单-001");
  await panel
    .getByLabel("核对依据")
    .fill("本地浏览器测试账单，确认此请求最终收费 45 微元。");
  await panel.getByLabel("本次调整原因").fill("解除未知请求费用占用");
  await expect(
    page.getByRole("button", { name: "返回项目", exact: true }),
  ).toBeDisabled();
  await panel.getByLabel("我已核对账单与此请求的对应关系").check();
  await panel.getByRole("button", { name: "确认保存账单记录" }).click();
  await expect(panel.getByRole("status")).toContainText("账单记录已保存");
  await expect(panel.locator(".adjustment")).toHaveCount(1);
  await expect(panel.locator(".adjustment")).toContainText("测试账单-001");
  await expect(
    page.getByRole("button", { name: "返回项目", exact: true }),
  ).toBeEnabled();
  await page.reload();
  await page.getByRole("button", { name: "模型与用途设置" }).click();
  await panel
    .getByRole("button", { name: new RegExp(fixture.project) })
    .click();
  await expect(panel.getByLabel("账单累计金额", { exact: true })).toHaveValue(
    "0.000045",
  );
  await expect(panel.locator(".adjustment")).toHaveCount(1);
  await expect(page.getByLabel("允许预算范围内的付费调用")).not.toBeChecked();
  await page.screenshot({
    path: "../.runtime/reconciliation.png",
    fullPage: true,
  });
});

test("到期账务说明展示保留证明，未知回执不显示为零收费", async ({ page }) => {
  const fixture = JSON.parse(
    execFileSync(
      "go",
      ["run", "-buildvcs=false", "./tests/fixtures/reconciliation"],
      {
        cwd: resolve(process.cwd(), "../backend"),
        encoding: "utf8",
        windowsHide: true,
      },
    ),
  ) as { id: string; project: string; currency: string };
  // UI contract fixture only; the real SQL expiry/history/replay proof is
  // exercised in tests/integration/retention_financial_test.go.
  await page.route(
    `**/api/model-calls/${fixture.id}/history`,
    async (route) => {
      await route.fulfill({
        json: {
          adjustments: [
            {
              id: "a".repeat(32),
              revision: 2,
              previous_amount_micros: 0,
              released_micros: 80,
              retention_state: "EXPIRED",
              expired_at: "2026-10-25T00:00:00Z",
              input: {
                amount_micros: 37,
                currency: fixture.currency,
                bill_reference: "",
                evidence: "",
                reason: "",
              },
            },
          ],
          receipts: [
            {
              id: "b".repeat(64),
              retention_state: "EXPIRED",
              outcome: "UNKNOWN",
              amount_micros: 0,
              expired_at: "2026-10-25T00:00:00Z",
            },
          ],
        },
      });
    },
  );
  await loginAsOwner(page);
  await page.getByRole("button", { name: "模型与用途设置" }).click();
  const panel = page.locator(".reconciliation-panel");
  await panel
    .getByRole("button", { name: new RegExp(fixture.project) })
    .click();
  await expect(panel.locator(".adjustment")).toContainText("已保留费用证明");
  await expect(panel.locator(".adjustment")).toContainText("0.000037");
  await expect(panel.locator(".adjustment")).toContainText(
    "原始账单说明已到保留期限",
  );
  await expect(panel.locator(".adjustment blockquote")).toHaveCount(0);
  await panel.locator("summary").click();
  await expect(panel.locator("details")).toContainText(
    "没有确切用量，不能按零收费判断",
  );
  await expect(panel.locator("details")).toContainText(
    "原始回执已到保留期限，费用证明仍保留",
  );
  await page.screenshot({
    path: "../.runtime/retention-financial-history.png",
    fullPage: true,
  });
});
