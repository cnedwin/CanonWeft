import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";
import { openIPSection, openKnowledgeView } from "./ip-edit-helper";

test("上传文字资料，经候选采用发布后做真实语义检索并回查原文", async ({
  page,
}) => {
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await loginAsOwner(page);
  const name = `知识导入 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("从文件导入资料", { exact: true }).click();
  const content = "🌙剑客进入山洞寻找失落的宝剑。\n洞穴深处有一条地下河。";
  await page.getByLabel("上传资料文件").setInputFiles({
    name: "洞穴记录.txt",
    mimeType: "text/plain",
    buffer: Buffer.from(content, "utf8"),
  });
  await expect(page.getByLabel("导入内容 · 可修改")).toHaveValue(content);
  await expect(
    page.getByText("已提取 1 个文字单元 · 待核对 0 项"),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "保存文件内容候选", exact: true })
    .click();
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  await expect(page.getByTestId("release-preview")).toContainText(content);
  await page
    .getByRole("button", { name: "确认采用整组内容", exact: true })
    .click();
  await page
    .getByRole("button", { name: "后台准备这组内容的发布索引", exact: true })
    .click();
  await expect(page.getByTestId("prospective-index-jobs")).toContainText(
    "索引已就绪",
    { timeout: 20000 },
  );
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  await expect(page.getByTestId("prospective-index-jobs")).toContainText(
    "索引已就绪",
  );
  await page.getByRole("button", { name: "发布整组内容", exact: true }).click();
  await openKnowledgeView(page, "索引任务");
  await page
    .getByRole("button", { name: "构建 / 更新检索索引", exact: true })
    .click();
  await expect(page.getByText("索引已就绪，共 1 个原文片段。")).toBeVisible();
  await openKnowledgeView(page, "检索资料");
  await page.getByLabel("要查找的线索").fill("侠客寻觅遗失的兵器");
  await page
    .getByRole("button", { name: "查找并保存证据包", exact: true })
    .click();
  await expect(page.locator(".evidence-card")).toContainText(
    "剑客进入山洞寻找失落的宝剑",
  );
  await page.getByRole("button", { name: "核对完整原文", exact: true }).click();
  await expect(
    page.getByText("经核对的完整原文", { exact: true }),
  ).toBeVisible();
  await page
    .getByText("指定必须参考的完整资料 · 已选 0 份", { exact: true })
    .click();
  await page.getByLabel("必须参考：洞穴记录.txt", { exact: true }).check();
  await page.getByLabel("要查找的线索").fill("无关的药剂条件");
  await page
    .getByRole("button", { name: "查找并保存证据包", exact: true })
    .click();
  const required = page.getByTestId("required-context");
  await expect(required).toContainText("必须参考的完整输入 · 1 项");
  await required.getByText("洞穴记录.txt · 原文依据", { exact: true }).click();
  await expect(required).toContainText(content);
  expect(pageErrors).toEqual([]);
  await page.screenshot({
    path: "../.runtime/knowledge-workflow.png",
    fullPage: true,
  });
});
