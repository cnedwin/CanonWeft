import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("两份共享资料作为联合候选确认发布，刷新后读取正式内容", async ({
  page,
}) => {
  await loginAsOwner(page);
  const name = `资料联合发布 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  for (const title of ["故事备忘", "时间记录"]) {
    await page.getByLabel("资料名称", { exact: true }).fill(title);
    await page.getByRole("button", { name: "创建资料", exact: true }).click();
    await page
      .getByRole("button", { name: `编辑 ${title}`, exact: true })
      .click();
    await page
      .getByLabel(`${title} · 待保存内容`, { exact: true })
      .fill(`${title}：陆遥在冬至抵达雪原。`);
  }
  await page.getByRole("button", { name: "保存联合候选", exact: true }).click();
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  await expect(page.getByTestId("release-preview")).toContainText(
    "陆遥在冬至抵达雪原。",
  );
  await page
    .getByRole("button", { name: "确认采用整组内容", exact: true })
    .click();
  await page.getByRole("button", { name: "发布整组内容", exact: true }).click();
  await expect(
    page.getByText("整组内容已发布，关联设定与资料保持同一版本。"),
  ).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await page
    .getByRole("button", { name: "编辑 故事备忘", exact: true })
    .click();
  await expect(
    page.getByLabel("故事备忘 · 待保存内容", { exact: true }),
  ).toHaveValue("故事备忘：陆遥在冬至抵达雪原。");
});
