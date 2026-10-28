import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("外部技能与起名素材可复制编辑，显示固定来源版本", async ({ page }) => {
  await loginAsOwner(page);
  await page
    .getByLabel("项目名称", { exact: true })
    .fill(`创作素材 ${Date.now()}`);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  await page.getByText("随系统提供的完整方法包", { exact: true }).click();
  const outline = page
    .locator("article")
    .filter({
      has: page.getByRole("heading", { name: "novel-outline", exact: true }),
    })
    .first();
  await expect(outline).toContainText("eternityspring/shuohao-skills");
  await expect(outline).toContainText("6dbddc64fed3");
  await outline.getByRole("button", { name: "复制到本项目后编辑" }).click();
  await expect(page.locator(".version-card")).toHaveCount(1);
  await expect(page.getByLabel("文件内容")).toHaveValue(/name: novel-outline/);
  const names = page
    .locator("article")
    .filter({
      has: page.getByRole("heading", { name: "character-naming", exact: true }),
    });
  await names.getByRole("button", { name: "复制到本项目后编辑" }).click();
  await expect(page.locator(".version-card")).toHaveCount(2);
  await expect(page.getByLabel("文件内容")).toHaveValue(
    /# 角色起名与名字素材整理/,
  );
  await page.screenshot({
    path: "../.runtime/creative-skill-catalog.png",
    fullPage: true,
  });
});

test("技能方法编辑保留旧稿，来源撤销阻止派生引用", async ({ page }) => {
  await loginAsOwner(page);
  await page
    .getByLabel("项目名称", { exact: true })
    .fill(`技能版本 ${Date.now()}`);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  await page.getByRole("button", { name: "手工编写 Skill" }).click();
  const original =
    "---\nname: evidence-method\ndescription: Use when checking story sources.\nlicense: LicenseRef-Private\n---\n区分陈述和事实，只提出候选；不能发布。";
  await page.getByLabel("新方法 SKILL.md").fill(original);
  await page.getByRole("button", { name: "保存方法候选" }).click();
  const versions = page.locator(".version-card");
  await expect(versions).toHaveCount(1);
  await versions.first().click();
  await expect(page.getByLabel("文件内容")).toHaveValue(original);
  await page.getByLabel("文件内容").fill(original + "\n证据必须关联原文版本。");
  await page.getByRole("button", { name: "保存为新技能版本" }).click();
  await expect(versions).toHaveCount(2);
  await expect(
    page.getByText("新版本已保存，原始包及上一个版本保持不变。"),
  ).toBeVisible();
  await page.getByRole("button", { name: "保存为方法参考版本" }).click();
  await expect(
    page.getByText(
      "该版本已保存为方法参考；这不代表模型效果或执行资格已通过。",
    ),
  ).toBeVisible();
  await versions.last().click();
  await expect(page.getByLabel("文件内容")).toHaveValue(original);
  await page.getByText("撤销此来源版本", { exact: true }).click();
  await page.getByLabel("撤销原因").fill("基线方法需要修订，停止派生引用");
  await page.getByRole("button", { name: "撤销此版本及派生引用" }).click();
  await expect(
    page.getByText(
      "该版本已撤销，所有派生版本的使用资格同时受限；历史内容保留。",
    ),
  ).toBeVisible();
  await versions.first().click();
  await expect(
    page.getByText("已保存为方法参考 · 本版本或上游已撤销", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "保存为方法参考版本" }),
  ).toHaveCount(0);
  await expect(page.getByLabel("文件内容")).toHaveValue(
    original + "\n证据必须关联原文版本。",
  );
  await page.getByText("随系统提供的完整方法包", { exact: true }).click();
  await page
    .locator("article")
    .filter({
      has: page.getByRole("heading", {
        name: "source-ledger-extract",
        exact: true,
      }),
    })
    .getByRole("button", { name: "复制到本项目后编辑" })
    .click();
  await expect(versions).toHaveCount(3);
  await expect(page.getByLabel("文件内容")).toHaveValue(
    /source-ledger-extract/,
  );
  await page.screenshot({
    path: "../.runtime/skills-library.png",
    fullPage: true,
  });
});
