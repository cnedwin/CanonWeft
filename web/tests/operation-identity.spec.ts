import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";

test("新请求使用无正文编号，成功回执丢失后重试复用同一编号", async ({
  page,
}) => {
  await loginAsOwner(page);
  const ids: string[] = [];
  const results: string[] = [];
  await page.route("**/api/projects", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    const input = route.request().postDataJSON() as { operation_id: string };
    ids.push(input.operation_id);
    const response = await route.fetch();
    const result = (await response.json()) as { id: string };
    results.push(result.id);
    if (ids.length === 1) {
      await route.abort("failed");
      return;
    }
    await route.fulfill({ response });
  });
  const name = `私人创作标题-${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("暂时无法连接");
  expect(ids[0]).toMatch(/^request-v2:[0-9a-f]{32}$/);
  expect(ids[0]).not.toContain(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(page.getByRole("button", { name, exact: true })).toBeVisible();
  expect(ids).toHaveLength(2);
  expect(ids[1]).toBe(ids[0]);
  expect(results[1]).toBe(results[0]);
});
