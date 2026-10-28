import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("专业角色预设可编辑，旧清单固定，撤销后不可试评", async ({ page }) => {
  await loginAsOwner(page);
  const projectName = `能力清单 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(projectName);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  const panel = page.locator(".capability-panel");
  await panel.getByRole("button", { name: "准备四个研究角色" }).click();
  await expect(panel.locator(".capability-version")).toHaveCount(9);
  await expect(panel.getByText(/固定 9 个能力版本/)).toBeVisible();
  await panel
    .getByRole("button", { name: /技能发现 Agent · 专业角色/ })
    .click();
  await expect(panel.getByText(/可进入受控文本试评/)).toBeVisible();
  const original = await panel.getByLabel("职责与执行方法").inputValue();
  const originalCard = await panel
    .getByRole("button", { name: /技能发现 Agent · 专业角色/ })
    .innerText();
  await panel
    .getByLabel("职责与执行方法")
    .fill(original + "\n新增：逐条核对候选来源。");
  await panel.getByRole("button", { name: "保存为新能力版本" }).click();
  await expect(panel.locator(".capability-version")).toHaveCount(10);
  await panel.getByRole("button", { name: originalCard, exact: true }).click();
  await expect(panel.getByLabel("职责与执行方法")).toHaveValue(original);
  await page.reload();
  await page.getByRole("button", { name: projectName }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  await panel.getByRole("button", { name: originalCard, exact: true }).click();
  await expect(panel.getByLabel("职责与执行方法")).toHaveValue(original);
  await panel.getByText("撤销能力版本", { exact: true }).click();
  await panel.getByLabel("能力撤销原因").fill("基线方法需重新检查");
  await panel
    .getByRole("button", { name: "撤销此能力版本", exact: true })
    .click();
  await expect(
    panel.getByText("此版本或其编辑祖先已撤销", { exact: true }),
  ).toBeVisible();
  await expect(panel.getByText(/当前不能直接执行/)).toBeVisible();
  await panel
    .getByRole("button", { name: /技能研究与适配工作流 · 工作流/ })
    .click();
  await expect(
    panel.getByText("此版本或其编辑祖先已撤销", { exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: "../.runtime/capability-manifest.png",
    fullPage: true,
  });
});
