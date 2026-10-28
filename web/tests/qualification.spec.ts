import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("独立评测真实任务费用证据、启用回退与撤销，并保留未提交表单", async ({
  page,
}) => {
  test.skip(
    process.env.STUDIO_QUALIFICATION_BROWSER_FIXTURE !== "1",
    "Requires the tests-only local protocol fixture executable; production server never fabricates provider output",
  );
  test.setTimeout(180000);
  await loginAsOwner(page);
  await expect(page.getByLabel("项目名称", { exact: true })).toBeVisible();
  const headers = { Origin: "http://127.0.0.1:18865" };
  const settings = await (await page.request.get("/api/models")).json();
  const saved = await page.request.post("/api/models", {
    headers,
    data: {
      provider: "deepseek",
      name: "browser qualification protocol fixture",
      model: "deepseek-flash",
      base_url: "https://api.deepseek.com",
      api_key: "tests-only-no-real-provider-key",
      enabled: true,
    },
  });
  expect(saved.status()).toBe(201);
  const connection = await saved.json();
  const previous = settings.bindings.find(
    (v: { role: string }) => v.role === "researcher",
  );
  expect(
    (
      await page.request.put("/api/model-bindings/researcher", {
        headers,
        data: {
          connection_id: connection.id,
          connection_revision: 1,
          revision: previous?.revision ?? 0,
        },
      })
    ).status(),
  ).toBe(200);
  expect(
    (
      await page.request.post("/api/model-prices", {
        headers,
        data: {
          connection_id: connection.id,
          connection_revision: 1,
          currency: "USD",
          input_micros_per_million: 1000000,
          output_micros_per_million: 1000000,
          cache_read_micros_per_million: 0,
          cache_write_micros_per_million: 0,
          source: "https://api-docs.deepseek.com/quick_start/pricing/",
          expires_at: new Date(Date.now() + 3600000)
            .toISOString()
            .replace(/\.\d{3}Z$/, "Z"),
        },
      })
    ).status(),
  ).toBe(201);
  const budget = await (await page.request.get("/api/model-budget")).json();
  expect(
    (
      await page.request.put("/api/model-budget", {
        headers,
        data: {
          ...budget,
          enabled: true,
          limit_micros: budget.spent_micros + budget.reserved_micros + 10000000,
          per_call_micros: 1000000,
        },
      })
    ).status(),
  ).toBe(200);
  const projectName = `独立资格 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(projectName);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  const panel = page.getByRole("region", { name: "独立评测与用途启用" });
  await panel
    .getByRole("button", { name: "准备可信初始对照", exact: true })
    .click();
  await expect(panel.getByLabel("固定候选版本")).not.toHaveValue("");
  await panel
    .getByRole("button", { name: "检查条件与费用估算", exact: true })
    .click();
  await expect(panel.getByText(/共 18 次独立执行/)).toBeVisible();
  await panel
    .getByRole("button", { name: "开始独立对照评测", exact: true })
    .click();
  await panel.getByLabel("精确输出上限（资格只适用于此参数）").fill("513");
  await expect(
    panel.getByRole("heading", { name: "机械用途资格通过", exact: true }),
  ).toBeVisible({ timeout: 90000 });
  await expect(
    panel.getByLabel("精确输出上限（资格只适用于此参数）"),
  ).toHaveValue("513");
  await panel
    .getByRole("button", { name: "启用此精确机械用途", exact: true })
    .click();
  await expect(panel.getByText(/当前选择 · 资格仍有效/)).toBeVisible();
  const report = await page.request.get(
    (await panel
      .getByRole("link", { name: "打开完整证据报告" })
      .getAttribute("href")) as string,
  );
  expect(report.status()).toBe(200);
  const proof = await report.json();
  expect(proof.steps).toHaveLength(18);
  expect(proof.professional_qualified).toBe(false);
  expect(
    proof.steps.every(
      (step: {
        child_id: string;
        metrics: { usage_known: boolean; cost_micros: number };
      }) =>
        step.child_id &&
        step.metrics.usage_known &&
        step.metrics.cost_micros > 0,
    ),
  ).toBe(true);
  await panel.getByRole("button", { name: "放弃评测表单修改" }).click();
  await expect(
    panel.getByLabel("精确输出上限（资格只适用于此参数）"),
  ).toHaveValue("512");
  await expect(
    panel.getByRole("button", { name: "放弃评测表单修改", exact: true }),
  ).toBeDisabled();
  const projects = await (await page.request.get("/api/projects")).json();
  const project = projects.items.find(
    (v: { name: string }) => v.name === projectName,
  );
  const bindingList = await (
    await page.request.get(`/api/projects/${project.id}/qualification/bindings`)
  ).json();
  const current = bindingList.items.find(
    (b: { current: boolean }) => b.current,
  );
  expect(
    (
      await page.request.post(
        `/api/projects/${project.id}/qualification/bindings`,
        {
          headers,
          data: {
            operation_id: crypto.randomUUID(),
            qualification_id: current.qualification_id,
            expected_binding: current.id,
            task_kind: "shared.contract",
            rollback: true,
          },
        },
      )
    ).status(),
  ).toBe(201);
  await panel.getByRole("button", { name: "刷新评测与启用历史" }).click();
  await expect(
    panel
      .locator(".binding")
      .filter({ hasText: "历史绑定" })
      .getByRole("button", { name: "回退到此仍合格版本", exact: true })
      .first(),
  ).toBeEnabled();
  await panel
    .locator(".binding")
    .filter({ hasText: "历史绑定" })
    .getByRole("button", { name: "回退到此仍合格版本", exact: true })
    .first()
    .click();
  await expect(
    panel.getByText("已回退到仍有效的用途资格；后续任务采用该绑定。", {
      exact: true,
    }),
  ).toBeVisible();
  await panel.getByText("撤销整个能力版本及依赖用途", { exact: true }).click();
  await panel.getByLabel("整个版本的撤销原因").fill("完整浏览器撤销回归");
  await panel
    .getByRole("button", { name: "撤销整个能力版本", exact: true })
    .click();
  await expect(
    panel.getByText(/已撤销整个能力版本，包含依赖它的资格/),
  ).toBeVisible();
  await expect(panel.getByText(/资格仍有效/)).toHaveCount(0);
  await page.screenshot({
    path: "../.runtime/qualification-browser.png",
    fullPage: true,
  });
});
