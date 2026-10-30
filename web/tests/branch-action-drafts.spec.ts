import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";
import { openIPSection, saveReasonedCandidate } from "./ip-edit-helper";

test("回流选择与逐项理由刷新恢复，影响入口读取实际清单", async ({ page }) => {
  await loginAsOwner(page);
  const project = `回流草稿 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(project);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByRole("button", { name: "新增角色或实体" }).click();
  await page.getByLabel("实体名称", { exact: true }).fill("林青");
  await page.getByLabel("实体简介").fill("原设定");
  await saveReasonedCandidate(page);
  await page.getByRole("button", { name: "确认采用此候选" }).first().click();
  await page.getByRole("button", { name: "发布此版本" }).first().click();
  await expect(page.getByText("正式版本已发布，旧版本仍保留。")).toBeVisible();
  await page.getByLabel("分支名称", { exact: true }).fill("改编");
  await page
    .getByLabel("分支来源")
    .selectOption({ label: "正史（当前已发布版本）" });
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByLabel("实体简介").fill("改编中补充的过往经历");
  await saveReasonedCandidate(page);
  await page
    .getByRole("button", { name: "查看这次变更的影响" })
    .first()
    .click();
  await expect(page.getByTestId("impact-plan")).toBeVisible();
  await page.getByRole("button", { name: "确认采用此候选" }).first().click();
  await page.getByRole("button", { name: "发布此版本" }).first().click();
  await expect(page.getByText("正式版本已发布，旧版本仍保留。")).toBeVisible();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  const promotion = page
    .locator("section")
    .filter({
      has: page.getByRole("heading", { name: "从改编回流设定", exact: true }),
    })
    .last();
  await promotion.getByLabel("回流来源").selectOption({ label: "改编" });
  await promotion.getByRole("button", { name: "读取改编正式版本" }).click();
  await promotion.getByLabel("回流实体 林青", { exact: true }).check();
  await promotion
    .getByLabel("回流理由", { exact: true })
    .fill("将确认的过往经历用于正史。");
  await expect(
    page.getByText("草稿已自动暂存，尚未采用", { exact: true }),
  ).toBeVisible();
  await promotion.getByRole("button", { name: "生成回流候选" }).click();
  await promotion
    .getByRole("textbox", { name: /^变更理由 / })
    .fill("补全角色过往；当前章节尚未改变。");
  await expect(
    page.getByText("草稿已自动暂存，尚未采用", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name: project, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await expect(promotion.getByLabel("回流来源")).not.toHaveValue("");
  await promotion.getByRole("button", { name: "读取改编正式版本" }).click();
  await expect(
    promotion.getByLabel("回流实体 林青", { exact: true }),
  ).toBeChecked();
  await expect(promotion.getByLabel("回流理由", { exact: true })).toHaveValue(
    "将确认的过往经历用于正史。",
  );
  await promotion.getByRole("button", { name: "生成回流候选" }).click();
  await expect(
    promotion.getByRole("textbox", { name: /^变更理由 / }),
  ).toHaveValue("补全角色过往；当前章节尚未改变。");
  await promotion.getByRole("button", { name: "生成回流候选" }).click();
  await expect(
    page.getByRole("button", { name: "确认采用此候选" }).first(),
  ).toBeDisabled();
  await expect(page.getByTestId("published-snapshot")).toContainText("原设定");
});
