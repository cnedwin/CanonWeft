import { loginAsOwner } from "./owner-session";
import { saveReasonedCandidate } from "./ip-edit-helper";
import { test, expect } from "@playwright/test";

test("规则例外缺证据时禁止采用，补齐后可发布并保留有效期", async ({ page }) => {
  await loginAsOwner(page);
  const name = `规则例外 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByRole("button", { name: "新增角色或实体" }).click();
  await page.getByLabel("实体名称", { exact: true }).fill("城中术士");
  await page.getByRole("button", { name: "新增设定断言" }).click();
  await page.getByLabel("设定属性").fill("力量");
  await page.getByLabel("设定内容").fill("20");
  await page.getByRole("button", { name: "新增世界规则" }).click();
  await page.getByLabel("规则名称").fill("城内力量限制");
  await page.getByLabel("约束属性").fill("力量");
  await page
    .getByRole("combobox", { name: "运算", exact: true })
    .selectOption("number_lte");
  await page.getByLabel("约束值").fill("10");
  await page.getByText("适用条件、有效期与例外", { exact: true }).click();
  await page.getByLabel("规则生效时间", { exact: true }).fill("0");
  await page.getByLabel("规则结束时间（不含）", { exact: true }).fill("10");
  await page.getByRole("button", { name: "添加明确例外", exact: true }).click();
  await page.getByLabel("例外名称").fill("特别许可");
  await page.getByLabel("条件属性").fill("许可");
  await page.getByLabel("条件值").fill("有");
  await saveReasonedCandidate(page);
  await page.getByRole("button", { name: "确认采用此候选" }).first().click();
  await expect(page.getByRole("alert")).toContainText("缺少可验证事实");
  await page.getByRole("button", { name: "新增设定断言" }).click();
  await page.getByLabel("设定属性").nth(1).fill("许可");
  await page.getByLabel("设定内容").nth(1).fill("有");
  const saved = page.waitForResponse(
    (response) =>
      response.url().endsWith("/candidates") &&
      response.request().method() === "GET",
  );
  await saveReasonedCandidate(page);
  await saved;
  await expect(
    page.getByRole("button", { name: "确认采用此候选" }),
  ).toHaveCount(2);
  await page.getByRole("button", { name: "确认采用此候选" }).first().click();
  await page.getByRole("button", { name: "发布此版本" }).first().click();
  await expect(page.getByText("正式版本已发布，旧版本仍保留。")).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await page.getByText("适用条件、有效期与例外", { exact: true }).click();
  await expect(
    page.getByLabel("规则结束时间（不含）", { exact: true }),
  ).toHaveValue("10");
  await expect(page.getByLabel("例外名称")).toHaveValue("特别许可");
});
