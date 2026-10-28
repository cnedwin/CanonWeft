import { loginAsOwner } from "./owner-session";
import { saveReasonedCandidate } from "./ip-edit-helper";
import { test, expect } from "@playwright/test";
test("职业功法结构、独立角色映射与发动条件可保存并验证", async ({ page }) => {
  await loginAsOwner(page);
  const name = `借火 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  for (const name of ["齐望", "唐栖", "齐栖", "借火术"]) {
    await page.getByRole("button", { name: "新增角色或实体" }).click();
    await page.getByLabel("实体名称", { exact: true }).last().fill(name);
  }
  await page.getByLabel("实体类型").last().selectOption("ability");
  await page.getByLabel("效果或职业职能", { exact: true }).fill("转移外部火焰");
  await page.getByLabel("代价", { exact: true }).fill("消耗体力");
  await page.getByLabel("限制", { exact: true }).fill("不能凭空生火");
  await page.getByRole("button", { name: "添加发动条件" }).click();
  await page.getByLabel("条件属性", { exact: true }).fill("接触火源");
  await page.getByLabel("条件值", { exact: true }).fill("是");
  await page.getByRole("button", { name: "添加身份映射" }).click();
  await page
    .getByLabel("改编实体", { exact: true })
    .selectOption({ label: "齐栖" });
  await page
    .getByLabel("来源实体", { exact: true })
    .selectOption([{ label: "齐望" }, { label: "唐栖" }]);
  await page.getByLabel("映射理由").fill("合并配角支线，保留原角色的独立认知");
  await saveReasonedCandidate(page);
  await page.getByRole("button", { name: "确认采用此候选" }).first().click();
  await page.getByRole("button", { name: "发布此版本" }).first().click();
  await expect(page.getByTestId("published-snapshot")).toContainText("借火术");
  const projects = await (await page.request.get("/api/projects")).json();
  const project = projects.items.find((p: { name: string }) => p.name === name);
  const branches = await (
    await page.request.get(`/api/projects/${project.id}/branches`)
  ).json();
  const branch = branches.items[0];
  const read = await (
    await page.request.get(`/api/projects/${project.id}/branches/${branch.id}`)
  ).json();
  const ability = read.snapshot.entities.find(
    (e: { name: string }) => e.name === "借火术",
  );
  expect(ability.profile.costs).toEqual(["消耗体力"]);
  expect(read.snapshot.mappings[0].sources).toHaveLength(2);
  const check = await (
    await page.request.get(
      `/api/projects/${project.id}/branches/${branch.id}/capabilities/${ability.id}/check?time=0`,
    )
  ).json();
  expect(check.status).toBe("UNKNOWN");
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await expect(page.getByLabel("限制", { exact: true })).toHaveValue(
    "不能凭空生火",
  );
  await page.getByLabel("分支名称", { exact: true }).fill("改编试验");
  await page
    .getByLabel("分支来源")
    .selectOption({ label: "正史（当前已发布版本）" });
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByRole("button", { name: "新增角色或实体" }).click();
  await page.getByLabel("实体名称", { exact: true }).last().fill("返照草");
  await page.getByLabel("实体类型").last().selectOption("item");
  await saveReasonedCandidate(page);
  await page.getByRole("button", { name: "确认采用此候选" }).first().click();
  await page.getByRole("button", { name: "发布此版本" }).first().click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await expect(page.getByTestId("published-snapshot")).not.toContainText(
    "返照草",
  );
  await page
    .getByLabel("回流来源", { exact: true })
    .selectOption({ label: "改编试验" });
  await page.getByRole("button", { name: "读取改编正式版本" }).click();
  await page.getByLabel("回流实体 返照草", { exact: true }).check();
  await page.getByLabel("回流理由").fill("将可复用药草设定纳入正史");
  await page.getByRole("button", { name: "生成回流候选" }).click();
  const reasons = page.getByRole("textbox", { name: /^变更理由 / });
  await expect(reasons.first()).toBeVisible();
  for (let i = 0; i < (await reasons.count()); i++)
    await reasons.nth(i).fill("保留改编中的明确设定，并独立审查其正史适用性。");
  await page.getByRole("button", { name: "生成回流候选" }).click();
  await expect(
    page.getByText(
      "改编回流提案：将可复用药草设定纳入正史。须由负责人确认采用。",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(page.getByTestId("published-snapshot")).not.toContainText(
    "返照草",
  );
  await expect(
    page.getByRole("button", { name: "确认采用此候选" }).first(),
  ).toBeDisabled();
  await expect(page.getByTestId("published-snapshot")).not.toContainText(
    "返照草",
  );
});
