import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";
import { openIPSection, openKnowledgeView } from "./ip-edit-helper";

test("私人工作区可持久构建、语义检索、核对原文并拒绝过期索引", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await loginAsOwner(page);
  const name = `私人语义 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByLabel("资料名称", { exact: true }).fill("母亲的药方");
  await page.getByRole("button", { name: "创建资料", exact: true }).click();
  await page
    .getByRole("button", { name: "编辑 母亲的药方", exact: true })
    .click();
  const text = "少女为治好母亲的顽疾，独自踏上采集灵药的旅途。🌙";
  await page.getByLabel("母亲的药方 · 待保存内容", { exact: true }).fill(text);
  await expect(
    page.getByText("草稿已暂存；发布后才成为正式资料"),
  ).toBeVisible();
  await openKnowledgeView(page, "索引任务");
  await page
    .getByRole("button", { name: "构建 / 更新检索索引", exact: true })
    .click();
  await expect(
    page.getByText("索引已就绪，共 0 个原文片段。", { exact: true }),
  ).toBeVisible();
  await openKnowledgeView(page, "检索资料");
  await page.getByLabel("明确纳入项目工作资料").check();
  await page
    .getByLabel("在我的草稿与可访问候选中进行语义检索", { exact: true })
    .check();
  await expect(page.getByTestId("private-working-index")).toContainText(
    "当前版本尚未建立私人索引",
  );
  await page
    .getByRole("button", { name: "构建 / 更新我的工作区索引", exact: true })
    .click();
  await expect(page.getByTestId("private-index-jobs")).toContainText(
    "索引已就绪",
    { timeout: 30000 },
  );
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openKnowledgeView(page, "检索资料");
  await page.getByLabel("明确纳入项目工作资料").check();
  await page
    .getByLabel("在我的草稿与可访问候选中进行语义检索", { exact: true })
    .check();
  await expect(page.getByTestId("private-index-jobs")).toContainText(
    "索引已就绪",
  );
  await page.getByLabel("要查找的线索").fill("女儿寻求救治家人的办法");
  await page
    .getByRole("button", { name: "查找并保存证据包", exact: true })
    .click();
  await expect(page.getByTestId("private-hits")).toContainText(text);
  await page
    .getByTestId("private-hits")
    .screenshot({ path: "../.runtime/private-index-hits.png" });
  await expect(
    page.getByRole("heading", { name: "原文证据 · 0 段", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "核对未确认原文", exact: true })
    .first()
    .click();
  await expect(
    page.getByText("经核对的完整原文", { exact: true }),
  ).toBeVisible();
  await openIPSection(page, "版本与分支");
  await page
    .getByRole("button", { name: "编辑 母亲的药方", exact: true })
    .click();
  await page
    .getByLabel("母亲的药方 · 待保存内容", { exact: true })
    .fill("她留在故乡照顾家人。");
  await expect(
    page.getByText("草稿已暂存；发布后才成为正式资料"),
  ).toBeVisible();
  await openKnowledgeView(page, "检索资料");
  await page
    .getByRole("button", { name: "查找并保存证据包", exact: true })
    .click();
  await expect(page.getByTestId("private-hits")).toHaveCount(0);
  await expect(page.locator(".knowledge-panel [role=alert]")).toBeVisible();
  expect(errors).toEqual([]);
});
