import { loginAsOwner } from "./owner-session";
import { saveReasonedCandidate } from "./ip-edit-helper";
import { test, expect } from "@playwright/test";

test("设定候选确认发布后可见，改编从固定版本创建，刷新保留", async ({
  page,
}) => {
  await loginAsOwner(page);
  const name = `雾城 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByRole("button", { name: "新增角色或实体" }).click();
  await page.getByLabel("实体名称", { exact: true }).fill("林舟");
  await page.getByLabel("实体简介").fill("雾城的青年医师");
  await page.getByRole("button", { name: "新增设定断言" }).click();
  await page.getByLabel("设定属性").fill("身份");
  await page.getByLabel("设定内容").fill("医师");
  await page.getByLabel("锁定此设定，发布后禁止直接改写").check();
  await expect(page.getByText("草稿已自动暂存，尚未采用")).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await expect(page.getByLabel("实体名称", { exact: true })).toHaveValue(
    "林舟",
  );
  await saveReasonedCandidate(page);
  await expect(page.getByText("正式版本尚无实体")).toBeVisible();
  await page.getByRole("button", { name: "确认采用此候选" }).first().click();
  await page.getByRole("button", { name: "发布此版本" }).first().click();
  await expect(page.getByTestId("published-snapshot")).toContainText("林舟");
  await expect(page.getByLabel("设定内容")).toBeDisabled();
  await page.getByLabel("提出锁定设定修订（须负责人专门批准）").check();
  await page
    .getByLabel("修订理由", { exact: true })
    .fill("明确前史中的职业状态，保留原版本");
  await page.getByLabel("设定内容").fill("隐退医师");
  await saveReasonedCandidate(page);
  await page.getByRole("button", { name: "批准锁定修订并采用" }).click();
  await page.getByRole("button", { name: "发布此版本" }).first().click();
  await expect(page.getByTestId("published-snapshot")).toContainText(
    "隐退医师",
  );
  await page.getByLabel("分支名称", { exact: true }).fill("影视改编");
  await page
    .getByLabel("分支来源")
    .selectOption({ label: "正史（当前已发布版本）" });
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await expect(page.getByTestId("published-snapshot")).toContainText("林舟");
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await expect(
    page.getByRole("button", { name: "影视改编", exact: true }),
  ).toBeVisible();
});
