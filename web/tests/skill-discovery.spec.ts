import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("发现来源版本冲突保留输入，停用后仍可编辑已有方法", async ({ page }) => {
  await loginAsOwner(page);
  await page
    .getByLabel("项目名称", { exact: true })
    .fill(`来源登记 ${Date.now()}`);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "允许来源内自动发现" }),
  ).toBeVisible();
  const registered = page.waitForResponse(
    (response) =>
      response.url().endsWith("/skill-discovery/sources") &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "登记来源" }).click();
  const receipt = await registered;
  const saved = await receipt.json();
  const source = page.locator(".discovery-source").first();
  await expect(source).toContainText("anthropics/skills");
  await expect(source).toContainText("已停用");
  await source.getByRole("button", { name: "编辑来源" }).click();
  await page.getByLabel("本地目录关键词").fill("my-unsaved-filter");
  await page.waitForTimeout(2500);
  await expect(page.getByLabel("本地目录关键词")).toHaveValue(
    "my-unsaved-filter",
  );
  const concurrent = await page.request.put(`${receipt.url()}/${saved.id}`, {
    headers: { Origin: new URL(receipt.url()).origin },
    data: {
      operation_id: crypto.randomUUID(),
      repository: saved.repository,
      ref: saved.ref,
      path: saved.path,
      keyword: "other-session",
      enabled: false,
      limit: saved.limit,
      expected_revision: saved.revision,
    },
  });
  expect(concurrent.status()).toBe(200);
  await page.getByRole("button", { name: "保存来源修改" }).click();
  await expect(page.locator(".discovery-panel [role=alert]")).toBeVisible();
  await expect(page.getByLabel("本地目录关键词")).toHaveValue(
    "my-unsaved-filter",
  );
  await page.getByRole("button", { name: "放弃来源输入" }).click();
  await expect(source).toContainText("other-session");
  await source.getByRole("button", { name: "编辑来源" }).click();
  await page.getByLabel("本地目录关键词").fill("my-unsaved-filter");
  await page.getByRole("button", { name: "保存来源修改" }).click();
  await expect(source).toContainText("my-unsaved-filter");
  await expect(source.getByRole("button", { name: "立即发现" })).toBeDisabled();
  await page.getByRole("button", { name: "手工编写 Skill" }).click();
  await page
    .getByLabel("新方法 SKILL.md")
    .fill(
      "---\nname: local-method\ndescription: Local evidence method.\n---\n本地输入不因自动轮询丢失。",
    );
  await page.waitForTimeout(2500);
  await expect(page.getByLabel("新方法 SKILL.md")).toHaveValue(/本地输入/);
  await page.getByRole("button", { name: "保存方法候选" }).click();
  await expect(page.locator(".version-card")).toHaveCount(1);
  await page.screenshot({
    path: "../.runtime/discovery-panel.png",
    fullPage: true,
  });
});
