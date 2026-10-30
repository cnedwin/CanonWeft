import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";
import { openIPSection } from "./ip-edit-helper";

test("工作资料分类可恢复并随候选发布，默认检索不把计划当作证据", async ({
  page,
}) => {
  await loginAsOwner(page);
  const name = `知识空间 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByLabel("资料名称", { exact: true }).fill("结局规划");
  await page.getByRole("button", { name: "创建资料", exact: true }).click();
  await page
    .getByRole("button", { name: "编辑 结局规划", exact: true })
    .click();
  await page.getByLabel("知识空间", { exact: true }).selectOption("working");
  await page
    .getByLabel("结局规划 · 待保存内容", { exact: true })
    .fill("结局规划：侠客将在最后一卷寻得失落的宝剑。");
  await expect(
    page.getByText("草稿已暂存；发布后才成为正式资料"),
  ).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page
    .getByRole("button", { name: "编辑 结局规划", exact: true })
    .click();
  await expect(page.getByLabel("知识空间", { exact: true })).toHaveValue(
    "working",
  );
  await openIPSection(page, "知识与任务");
  await page
    .getByRole("button", { name: "构建 / 更新检索索引", exact: true })
    .click();
  await expect(
    page.getByText("索引已就绪，共 0 个原文片段。", { exact: true }),
  ).toBeVisible();
  await page.getByLabel("明确纳入项目工作资料").check();
  await page.getByLabel("我的资料草稿：结局规划", { exact: true }).check();
  await page.getByLabel("要查找的线索").fill("完善这个结局计划");
  await page
    .getByRole("button", { name: "查找并保存证据包", exact: true })
    .click();
  const unconfirmed = page.getByTestId("unconfirmed-context");
  await expect(unconfirmed).toContainText("未确认资料 · 1 项");
  await unconfirmed.getByText("结局规划 · 私人草稿", { exact: true }).click();
  await expect(unconfirmed).toContainText("侠客将在最后一卷寻得失落的宝剑。");
  await expect(
    page.getByRole("heading", { name: "原文证据 · 0 段", exact: true }),
  ).toBeVisible();
  await page.getByLabel("明确纳入项目工作资料").uncheck();
  await openIPSection(page, "版本与分支");
  await page.getByRole("button", { name: "保存联合候选", exact: true }).click();
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  await expect(page.getByTestId("release-preview")).toContainText(
    "项目工作资料",
  );
  await page
    .getByRole("button", { name: "确认采用整组内容", exact: true })
    .click();
  await page.getByRole("button", { name: "发布整组内容", exact: true }).click();
  await expect(
    page.getByText("整组内容已发布，关联设定与资料保持同一版本。"),
  ).toBeVisible();
  await openIPSection(page, "知识与任务");
  await page.getByRole("button", { name: "构建 / 更新检索索引" }).click();
  await expect(page.getByText("索引已就绪，共 1 个原文片段。")).toBeVisible();
  await page.getByLabel("要查找的线索").fill("宝剑");
  await page.getByRole("button", { name: "查找并保存证据包" }).click();
  await expect(
    page.getByRole("heading", { name: "原文证据 · 0 段" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "项目工作资料（不作为已发生事实）" }),
  ).toHaveCount(0);
  await page.getByLabel("明确纳入项目工作资料").check();
  await page.getByRole("button", { name: "查找并保存证据包" }).click();
  await expect(
    page.getByRole("heading", { name: "项目工作资料（不作为已发生事实）" }),
  ).toBeVisible();
  await expect(page.locator(".knowledge-result")).toContainText(
    "侠客将在最后一卷寻得失落的宝剑。",
  );
});
