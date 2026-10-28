import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("人工资料候选展示真实来源并可确认", async ({ page }) => {
  await loginAsOwner(page);
  await page
    .getByLabel("项目名称", { exact: true })
    .fill(`人工来源 ${Date.now()}`);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByLabel("资料名称", { exact: true }).fill("人工笔记");
  await page.getByRole("button", { name: "创建资料", exact: true }).click();
  await page
    .getByRole("button", { name: "编辑 人工笔记", exact: true })
    .click();
  await page
    .getByLabel("人工笔记 · 待保存内容", { exact: true })
    .fill("从访谈中人工整理的参考记录。");
  await page.getByRole("button", { name: "保存联合候选", exact: true }).click();
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  const preview = page.getByTestId("release-preview");
  await expect(preview.getByRole("region", { name: "候选来源" })).toContainText(
    "来源：人工整理的参考资料",
  );
  await expect(preview).toContainText(
    "没有将人工整理或文件导入标为 AI 审核通过",
  );
  await expect(
    preview.getByRole("button", { name: "确认采用整组内容", exact: true }),
  ).toBeEnabled();
  await preview
    .getByRole("button", { name: "确认采用整组内容", exact: true })
    .click();
  await expect(
    preview.getByRole("button", { name: "发布整组内容", exact: true }),
  ).toBeEnabled();
});

test("选择实际生成来源后改稿刷新仍保留血缘并阻止沿用旧资格", async ({
  page,
}) => {
  test.skip(
    process.env.STUDIO_QUALIFICATION_BROWSER_FIXTURE !== "1",
    "Requires tests-only local protocol transport; never invokes a real provider",
  );
  test.setTimeout(120000);
  await loginAsOwner(page);
  await expect(page.getByLabel("项目名称", { exact: true })).toBeVisible();
  const headers = { Origin: "http://127.0.0.1:18865" };
  const settings = await (await page.request.get("/api/models")).json();
  const connectionResponse = await page.request.post("/api/models", {
    headers,
    data: {
      provider: "deepseek",
      name: "release source protocol fixture",
      model: "deepseek-flash",
      base_url: "https://api.deepseek.com",
      api_key: "tests-only-not-a-real-provider-key",
      enabled: true,
    },
  });
  expect(connectionResponse.status()).toBe(201);
  const connection = await connectionResponse.json();
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
  const priceResponse = await page.request.post("/api/model-prices", {
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
  });
  expect(priceResponse.status()).toBe(201);
  const price = await priceResponse.json();
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
  try {
    const name = `生成来源 ${Date.now()}`;
    await page.getByLabel("项目名称", { exact: true }).fill(name);
    const created = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" && r.url().endsWith("/api/projects"),
    );
    await page.getByRole("button", { name: "创建项目", exact: true }).click();
    const project = await (await created).json();
    await page.getByRole("button", { name: "打开设定库" }).click();
    await page.getByLabel("分支名称", { exact: true }).fill("正史");
    const branched = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith(`/projects/${project.id}/branches`),
    );
    await page.getByRole("button", { name: "创建分支", exact: true }).click();
    const branch = await (await branched).json();
    const root = `/api/projects/${project.id}`;
    const prepared = await page.request.post(`${root}/generations`, {
      headers,
      data: {
        branch_id: branch.id,
        expected_ip_head: branch.head,
        role: "researcher",
        price_id: price.id,
        operation_id: `release-source-${Date.now()}`,
        request: {
          prompt: 'Input JSON:\n{"source":"真实服务流水线生成的测试资料"}',
          max_output_tokens: 128,
        },
      },
    });
    expect(prepared.status()).toBe(201);
    const job = await prepared.json();
    expect(
      (
        await page.request.post(`${root}/generations/${job.id}/start`, {
          headers,
          data: {},
        })
      ).status(),
    ).toBe(200);
    await expect
      .poll(
        async () =>
          (
            await (
              await page.request.get(`${root}/generations/${job.id}`)
            ).json()
          ).state,
        { timeout: 60000 },
      )
      .toBe("SUCCEEDED");
    await page.getByLabel("资料名称", { exact: true }).fill("带来源资料");
    await page.getByRole("button", { name: "创建资料", exact: true }).click();
    await page
      .getByRole("button", { name: "编辑 带来源资料", exact: true })
      .click();
    await page.getByText("从已有 AI 结果建立资料来源", { exact: true }).click();
    await page
      .getByRole("button", { name: "加载已完成的生成结果", exact: true })
      .click();
    await page.getByLabel("生成来源", { exact: true }).selectOption(job.id);
    await expect(
      page.getByText("该结果可以保存为待复核候选，目前不能作为获准结果发布。"),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "保存来源候选并载入编辑区", exact: true })
      .click();
    const text = page.getByLabel("带来源资料 · 待保存内容", { exact: true });
    await expect(text).toHaveValue(/真实服务流水线生成的测试资料/);
    await text.fill("人工继续修改了这份生成资料。");
    await page
      .getByRole("button", { name: "保存联合候选", exact: true })
      .click();
    await expect(
      page.getByText("联合候选已保存。请展开对照检查，再确认整组内容。", {
        exact: true,
      }),
    ).toBeVisible();
    await page.reload();
    await page.getByRole("button", { name, exact: true }).click();
    await page.getByRole("button", { name: "打开设定库" }).click();
    await page.getByRole("button", { name: "正史", exact: true }).click();
    await page
      .getByRole("button", { name: "编辑 带来源资料", exact: true })
      .click();
    await expect(
      page.getByLabel("带来源资料 · 待保存内容", { exact: true }),
    ).toHaveValue("人工继续修改了这份生成资料。");
    await expect(
      page.getByText(new RegExp(`此资料已记录生成来源.*${job.id}`)),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "检查联合候选", exact: true })
      .first()
      .click();
    const preview = page.getByTestId("release-preview");
    await expect(preview).toContainText("来源：基于已有来源修改");
    await expect(preview).toContainText(
      "旧资格对应旧内容，当前修改需要独立复核",
    );
    await expect(
      preview.getByRole("button", { name: "确认采用整组内容", exact: true }),
    ).toBeDisabled();
    await expect(
      preview.getByRole("button", { name: "发布整组内容", exact: true }),
    ).toHaveCount(0);
    const releases = await (
      await page.request.get(`${root}/releases?branch=${branch.id}`)
    ).json();
    const edited = releases.items.find(
      (r: { manifest: { artifacts: { origin?: { kind: string } }[] } }) =>
        r.manifest.artifacts[0].origin?.kind === "derived",
    );
    expect(edited.manifest.artifacts[0].origin.generation.generation_id).toBe(
      job.id,
    );
    expect(
      (
        await page.request.post(`${root}/releases/${edited.id}/adopt`, {
          headers,
          data: {},
        })
      ).status(),
    ).toBe(503);
  } finally {
    const current = await (await page.request.get("/api/model-budget")).json();
    expect(
      (
        await page.request.put("/api/model-budget", {
          headers,
          data: { ...current, enabled: false },
        })
      ).status(),
    ).toBe(200);
  }
});
