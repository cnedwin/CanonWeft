import { loginAsOwner } from "./owner-session";
import { saveReasonedCandidate } from "./ip-edit-helper";
import { test, expect, type Page } from "@playwright/test";

async function publishEdit(page: Page) {
  await saveReasonedCandidate(page);
  await page.getByRole("button", { name: "确认采用此候选" }).first().click();
  await page.getByRole("button", { name: "发布此版本" }).first().click();
  await expect(page.getByText("正式版本已发布，旧版本仍保留。")).toBeVisible();
}
test("改编冲突选择刷新保留，未独立审查不改变正式版本", async ({ page }) => {
  await loginAsOwner(page);
  const projectName = `九层塔 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(projectName);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByRole("button", { name: "新增角色或实体" }).click();
  await page.getByLabel("实体名称", { exact: true }).fill("谢青");
  await page.getByLabel("实体简介").fill("守塔人");
  await page.getByRole("button", { name: "新增设定断言" }).click();
  await page.getByLabel("设定属性").fill("境界");
  await page.getByLabel("设定内容").fill("3");
  await page.getByRole("button", { name: "新增世界规则" }).click();
  await page.getByLabel("规则名称").fill("九层上限");
  await page.getByLabel("约束属性").fill("境界");
  await page
    .getByRole("combobox", { name: "运算", exact: true })
    .selectOption("number_lte");
  await page.getByLabel("约束值").fill("9");
  await page.getByRole("button", { name: "新增故事事件" }).click();
  await page.getByLabel("事件名称").fill("入塔");
  await page.getByLabel("事件描述").fill("谢青接受守塔职责");
  await publishEdit(page);
  await expect(page.getByTestId("published-snapshot")).toContainText(
    "九层上限",
  );
  await expect(page.getByTestId("published-snapshot")).toContainText("入塔");
  await page.getByLabel("分支名称", { exact: true }).fill("改编");
  await page
    .getByLabel("分支来源")
    .selectOption({ label: "正史（当前已发布版本）" });
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByLabel("实体简介").fill("改编中的少年守塔人");
  await publishEdit(page);
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await page.getByLabel("实体简介").fill("正史中的隐退守塔人");
  await publishEdit(page);
  await page.getByRole("button", { name: "改编", exact: true }).click();
  await page.getByRole("button", { name: "比较上游变化" }).click();
  await page.getByLabel("冲突处理").selectOption("local");
  await expect(
    page.getByText("草稿已自动暂存，尚未采用", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name: projectName, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "改编", exact: true }).click();
  await page.getByRole("button", { name: "比较上游变化" }).click();
  await expect(page.getByLabel("冲突处理")).toHaveValue("local");
  await page.getByRole("button", { name: "保存合并候选" }).click();
  await expect(
    page.getByRole("button", { name: "确认采用此候选" }).first(),
  ).toBeDisabled();
  await expect(page.getByTestId("published-snapshot")).toContainText(
    "改编中的少年守塔人",
  );
  await page.getByRole("button", { name: "比较上游变化" }).click();
  await expect(page.getByLabel("冲突处理")).toBeVisible();
});
