import { loginAsOwner } from "./owner-session";
import { test, expect, type Page } from "@playwright/test";
import { openIPSection } from "./ip-edit-helper";

const headers = { Origin: "http://127.0.0.1:18865" };
async function project(page: Page, name: string) {
  await loginAsOwner(page);
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  const created = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/api/projects"),
  );
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  const p = await (await created).json();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  const branched = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      r.url().endsWith(`/projects/${p.id}/branches`),
  );
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  return { project: p, branch: await (await branched).json() };
}
async function post(page: Page, path: string, data: unknown, status = 201) {
  const r = await page.request.post(path, { headers, data });
  expect(r.status(), (await r.text()).slice(0, 500)).toBe(status);
  return r.json();
}

test("人工逐项理由刷新保留且无模型可采用", async ({ page }) => {
  const name = `逐项人工 ${Date.now()}`;
  await project(page, name);
  await page
    .getByRole("button", { name: "新增角色或实体", exact: true })
    .click();
  await page.getByLabel("实体名称", { exact: true }).fill("林青");
  await page.getByLabel("实体简介", { exact: true }).fill("守护旧城的医师");
  await expect(
    page.getByText("草稿已自动暂存，尚未采用", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "检查变更并保存候选", exact: true })
    .click();
  const reason = page.getByRole("textbox", { name: /^变更理由 / }).first();
  await reason.fill("确立主角身份，后续人物关系以此为基础。");
  await expect(
    page.getByText("草稿已自动暂存，尚未采用", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "设定草稿");
  await page
    .getByRole("button", { name: "检查变更并保存候选", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: /^变更理由 / }).first(),
  ).toHaveValue("确立主角身份，后续人物关系以此为基础。");
  await page
    .getByRole("button", { name: "保存为候选版本", exact: true })
    .click();
  await page
    .getByRole("button", { name: "确认采用此候选", exact: true })
    .first()
    .click();
  await expect(
    page.getByRole("button", { name: "发布此版本", exact: true }).first(),
  ).toBeEnabled();
  await page
    .getByRole("button", { name: "打开独立语义审查", exact: true })
    .first()
    .click();
  const review = page.getByTestId("semantic-review-panel").last();
  await expect(review).toContainText("不强制调用模型");
  await review
    .getByRole("button", { name: "保存并准备当前内容审查", exact: true })
    .click();
  await expect(review).toContainText("等待条件恢复");
  await expect(review).toContainText("尚未审查");
});

test("派生资料独立审查后明确选择再采用发布", async ({ page }) => {
  test.skip(
    process.env.STUDIO_QUALIFICATION_BROWSER_FIXTURE !== "1",
    "Only the tests-only provider transport is allowed",
  );
  test.setTimeout(240000);
  const name = `独立审查 ${Date.now()}`;
  const setup = await project(page, name);
  const root = `/api/projects/${setup.project.id}`;
  const settings = await (await page.request.get("/api/models")).json();
  const connection = await post(page, "/api/models", {
    provider: "deepseek",
    name: "semantic browser protocol",
    model: "deepseek-flash",
    base_url: "https://api.deepseek.com",
    api_key: "tests-only-no-real-key",
    enabled: true,
  });
  for (const role of ["researcher", "critic"]) {
    const previous = settings.bindings.find(
      (b: { role: string }) => b.role === role,
    );
    const r = await page.request.put(`/api/model-bindings/${role}`, {
      headers,
      data: {
        connection_id: connection.id,
        connection_revision: 1,
        revision: previous?.revision ?? 0,
      },
    });
    expect(r.status(), await r.text()).toBe(200);
  }
  const price = await post(page, "/api/model-prices", {
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
  });
  const originalBudget = await (
    await page.request.get("/api/model-budget")
  ).json();
  expect(
    (
      await page.request.put("/api/model-budget", {
        headers,
        data: {
          ...originalBudget,
          enabled: true,
          limit_micros:
            originalBudget.spent_micros +
            originalBudget.reserved_micros +
            10000000,
          per_call_micros: 1000000,
        },
      })
    ).status(),
  ).toBe(200);
  try {
    const preset = await post(page, `${root}/qualification/baseline-presets`, {
      suite_id: "shared-semantic-review-v1",
      kind: "agent",
      role: "critic",
    });
    const qualification = await post(
      page,
      `${root}/qualification/runs`,
      {
        operation_id: crypto.randomUUID(),
        version_id: preset.baseline_id,
        baseline_id: preset.baseline_id,
        branch_id: setup.branch.id,
        expected_ip_head: "",
        expected_binding: "",
        suite_id: "shared-semantic-review-v1",
        role: "critic",
        price_id: price.id,
        max_output_tokens: 1024,
      },
      202,
    );
    await expect
      .poll(
        async () =>
          (
            await (
              await page.request.get(
                `${root}/qualification/runs/${qualification.id}`,
              )
            ).json()
          ).state,
        { timeout: 120000 },
      )
      .toBe("PASSED");
    const qualified = await (
      await page.request.get(`${root}/qualification/runs/${qualification.id}`)
    ).json();
    await post(page, `${root}/qualification/bindings`, {
      operation_id: crypto.randomUUID(),
      qualification_id: qualified.qualification_id,
      task_kind: "shared.semantic-review",
      expected_binding: "",
    });
    const generation = await post(page, `${root}/generations`, {
      branch_id: setup.branch.id,
      expected_ip_head: "",
      role: "researcher",
      price_id: price.id,
      operation_id: crypto.randomUUID(),
      request: {
        prompt: 'Input JSON:\n{"source":"独立候选资料"}',
        max_output_tokens: 128,
      },
    });
    await post(page, `${root}/generations/${generation.id}/start`, {}, 200);
    await expect
      .poll(
        async () =>
          (
            await (
              await page.request.get(`${root}/generations/${generation.id}`)
            ).json()
          ).state,
        { timeout: 60000 },
      )
      .toBe("SUCCEEDED");
    const resource = await post(page, `${root}/resources`, {
      branch_id: setup.branch.id,
      name: "审查资料",
      kind: "note",
      operation_id: crypto.randomUUID(),
    });
    const release = await post(page, `${root}/releases`, {
      branch_id: setup.branch.id,
      operation_id: crypto.randomUUID(),
      updates: [
        {
          resource_id: resource.id,
          text: "对实际生成资料做了人工改写，需要检查新内容。",
          origin: { generation_id: generation.id },
        },
      ],
    });
    await page.reload();
    await page.getByRole("button", { name, exact: true }).click();
    await page.getByRole("button", { name: "打开设定库" }).click();
    await page.getByRole("button", { name: "正史", exact: true }).click();
    await openIPSection(page, "版本与分支");
    await page
      .getByRole("button", { name: "检查联合候选", exact: true })
      .first()
      .click();
    const preview = page.getByTestId("release-preview"),
      review = preview.getByTestId("semantic-review-panel");
    await expect(
      preview.getByRole("button", { name: "确认采用整组内容", exact: true }),
    ).toBeDisabled();
    await review
      .getByRole("button", { name: "保存并准备当前内容审查", exact: true })
      .click();
    await expect(review).toContainText("已准备，尚未调用");
    await review
      .getByRole("button", { name: "按估算启动或继续审查", exact: true })
      .click();
    await expect(review).toContainText("报告已生成", { timeout: 60000 });
    await review
      .getByRole("button", { name: "确认选择此审查报告", exact: true })
      .click();
    await expect(
      preview.getByRole("button", { name: "确认采用整组内容", exact: true }),
    ).toBeEnabled();
    await preview
      .getByRole("button", { name: "确认采用整组内容", exact: true })
      .click();
    await preview
      .getByRole("button", { name: "发布整组内容", exact: true })
      .click();
    await expect
      .poll(
        async () =>
          (
            await (
              await page.request.get(`${root}/releases/${release.id}`)
            ).json()
          ).status,
      )
      .toBe("PUBLISHED");
  } finally {
    const budget = await (await page.request.get("/api/model-budget")).json();
    await page.request.put("/api/model-budget", {
      headers,
      data: { ...budget, enabled: false },
    });
  }
});
