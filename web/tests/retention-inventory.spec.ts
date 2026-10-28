import { expect, test } from "@playwright/test";
import { loginAsOwner } from "./owner-session";

test("资料归属检查读取实际记录，失败时不保留旧的成功统计", async ({
  page,
  request,
}) => {
  expect(
    (await request.get("/api/operations/retention-inventory")).status(),
  ).toBe(401);
  await loginAsOwner(page);
  await page.getByLabel("项目名称", { exact: true }).fill("未保存的写作计划");
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  const reportPromise = page.waitForResponse((response) =>
    response.url().endsWith("/operations/retention-inventory"),
  );
  await page.getByRole("button", { name: "检查资料归属", exact: true }).click();
  const response = await reportPromise;
  expect(response.status()).toBe(200);
  const report = await response.json();
  expect(report.state).toBe("INVENTORIED");
  expect(report.deletion_ready).toBe(false);
  expect(report.project_count).toBeGreaterThan(0);
  expect(report.project_records).toBeGreaterThan(0);
  expect(report.adopted_content_references).toBeGreaterThanOrEqual(0);
  expect(report.adopted_content_objects).toBeGreaterThanOrEqual(0);
  await expect(
    page.getByText("已采用内容引用（含历史版本）", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("作品专属记录", { exact: true })).toBeVisible();
  await expect(page.getByText("工作室共享记录", { exact: true })).toBeVisible();
  await page.screenshot({
    path: "../.runtime/retention-inventory.png",
    fullPage: true,
  });
  await page.route("**/api/operations/retention-inventory", (route) =>
    route.fulfill({ status: 503, json: { error: "资料归属需要检查" } }),
  );
  await page.getByRole("button", { name: "检查资料归属", exact: true }).click();
  await expect(
    page.getByText("资料归属需要检查", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("作品专属记录", { exact: true }),
  ).not.toBeVisible();
  await page.getByRole("button", { name: "返回工作室", exact: true }).click();
  await expect(page.getByLabel("项目名称", { exact: true })).toHaveValue(
    "未保存的写作计划",
  );
});
