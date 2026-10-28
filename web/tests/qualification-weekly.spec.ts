import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("每周评测保存范围重挂载可直接停用，范围替换明确且周开关金额保护未保存修改", async ({
  page,
}) => {
  test.skip(
    process.env.STUDIO_QUALIFICATION_BROWSER_FIXTURE !== "1",
    "Requires tests-only qualification fixture; no provider network",
  );
  await loginAsOwner(page);
  await expect(page.getByLabel("项目名称", { exact: true })).toBeVisible();
  const headers = { Origin: "http://127.0.0.1:18865" };
  const name = `周评测保存范围 ${Date.now()}`;
  const project = await (
    await page.request.post("/api/projects", {
      headers,
      data: { name, operation_id: crypto.randomUUID() },
    })
  ).json();
  const base = `/api/projects/${project.id}`;
  const branch = await (
    await page.request.post(`${base}/branches`, {
      headers,
      data: { name: "独立评测线", operation_id: crypto.randomUUID() },
    })
  ).json();
  const preset = await (
    await page.request.post(`${base}/qualification/baseline-presets`, {
      headers,
      data: {
        suite_id: "shared-contract-v1",
        kind: "agent",
        role: "researcher",
      },
    })
  ).json();
  const optionList = await (
    await page.request.get(`${base}/generation-options`)
  ).json();
  const scope = {
    operation_id: "",
    version_id: preset.baseline_id,
    baseline_id: preset.baseline_id,
    branch_id: branch.id,
    expected_ip_head: branch.head,
    expected_binding: "",
    suite_id: "shared-contract-v1",
    role: "researcher",
    price_id:
      optionList.items.find((v: { role: string }) => v.role === "researcher")
        ?.price.id ?? "",
    max_output_tokens: 512,
  };
  expect(
    (
      await page.request.put(`${base}/qualification/schedule`, {
        headers,
        data: {
          input: scope,
          enabled: true,
          budget_limit_micros: 0,
          revision: 0,
        },
      })
    ).status(),
  ).toBe(200);
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  const panel = page.getByRole("region", { name: "独立评测与用途启用" });
  await panel
    .getByText("项目每周评测（默认关闭付费）", { exact: true })
    .click();
  const weekly = panel.locator("details").filter({
    has: page.getByText("项目每周评测（默认关闭付费）", { exact: true }),
  });
  await expect(weekly.getByRole("checkbox")).toBeChecked();
  await weekly.getByRole("checkbox").uncheck();
  await expect(
    weekly.getByRole("button", { name: "保存每周评测设置", exact: true }),
  ).toBeEnabled();
  let releaseSave!: () => void;
  const pausedSave = new Promise<void>((resolve) => {
    releaseSave = resolve;
  });
  let sawSave!: () => void;
  const saveStarted = new Promise<void>((resolve) => {
    sawSave = resolve;
  });
  await page.route(`**${base}/qualification/schedule`, async (route) => {
    if (route.request().method() === "PUT") {
      sawSave();
      await pausedSave;
    }
    await route.continue();
  });
  try {
    await weekly
      .getByRole("button", { name: "保存每周评测设置", exact: true })
      .click();
    await saveStarted;
    await expect(weekly.getByRole("checkbox")).toBeDisabled();
    await expect(
      weekly.getByLabel("每周最多使用的币种金额（0 表示禁止收费）"),
    ).toBeDisabled();
    await expect(
      weekly.getByRole("button", {
        name: "使用上方评测范围替换周计划",
        exact: true,
      }),
    ).toBeDisabled();
    await expect(
      weekly.getByRole("button", { name: "放弃周评测修改", exact: true }),
    ).toBeDisabled();
  } finally {
    releaseSave();
  }
  await expect(panel.getByText(/已保存项目每周评测设置/)).toBeVisible();
  await page.unrouteAll({ behavior: "wait" });
  let stored = await (
    await page.request.get(`${base}/qualification/schedule`)
  ).json();
  expect(stored.enabled).toBe(false);
  expect(stored.input.version_id).toBe(preset.baseline_id);
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  await panel
    .getByText("项目每周评测（默认关闭付费）", { exact: true })
    .click();
  await expect(
    weekly.getByRole("group", { name: "每周评测固定范围", exact: true }),
  ).toContainText(preset.baseline_id);
  await panel.getByLabel("可信评测集").selectOption("source-ledger-v1");
  await weekly
    .getByRole("button", { name: "保存每周评测设置", exact: true })
    .click();
  stored = await (
    await page.request.get(`${base}/qualification/schedule`)
  ).json();
  expect(stored.input.suite_id).toBe("shared-contract-v1");
  expect(stored.input.version_id).toBe(preset.baseline_id);
  await panel
    .getByRole("button", { name: "放弃评测表单修改", exact: true })
    .click();
  await weekly
    .getByLabel("每周最多使用的币种金额（0 表示禁止收费）")
    .fill("0.25");
  await weekly.getByRole("checkbox").check();
  await page.getByRole("button", { name: "返回项目书架", exact: true }).click();
  await expect(page.getByText(/当前修改尚未保存/)).toBeVisible();
  await expect(panel).toBeVisible();
  await weekly
    .getByRole("button", { name: "放弃周评测修改", exact: true })
    .click();
  await expect(weekly.getByRole("checkbox")).not.toBeChecked();
  await expect(
    weekly.getByLabel("每周最多使用的币种金额（0 表示禁止收费）"),
  ).toHaveValue("0");
  await page.getByRole("button", { name: "返回项目书架", exact: true }).click();
  await expect(page.getByLabel("项目名称", { exact: true })).toBeVisible();
});
