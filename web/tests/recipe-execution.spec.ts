import { test, expect, type Page } from "@playwright/test";
import { loginAsOwner } from "./owner-session";
const headers = { Origin: "http://127.0.0.1:18865" };
async function post(page: Page, path: string, data: unknown, status = 201) {
  const r = await page.request.post(path, { headers, data });
  expect(r.status(), (await r.text()).slice(0, 400)).toBe(status);
  return r.json();
}
test("工作流逐步确认、刷新恢复、重做保留费用与历史", async ({ page }) => {
  test.skip(
    process.env.STUDIO_QUALIFICATION_BROWSER_FIXTURE !== "1",
    "Requires the tests-only local provider transport",
  );
  test.setTimeout(180000);
  await loginAsOwner(page);
  const name = `工作流浏览器 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  const created = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/api/projects"),
  );
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  const project = await (await created).json(),
    root = `/api/projects/${project.id}`;
  const branch = await post(page, `${root}/branches`, {
    name: "主线",
    operation_id: crypto.randomUUID(),
  });
  const definition = {
    kind: "agent",
    key: "browser-writer",
    name: "浏览器阶段角色",
    role: "writer",
    description: "本地协议验证",
    instructions: "按指定资料返回候选摘要，等待作者确认。",
    input_schema: {
      type: "object",
      properties: { source: { type: "string" } },
      required: ["source"],
      additionalProperties: false,
    },
    output_schema: {
      type: "object",
      properties: {
        summary: { type: "string" },
        rejected: { type: "boolean" },
      },
      required: ["summary", "rejected"],
      additionalProperties: false,
    },
    dependencies: [],
  };
  const agent = await post(page, `${root}/capabilities`, {
    operation_id: crypto.randomUUID(),
    definition,
  });
  const recipe = await post(page, `${root}/capabilities`, {
    operation_id: crypto.randomUUID(),
    definition: {
      ...definition,
      kind: "recipe",
      key: "browser-recipe",
      name: "浏览器完整工作流",
      role: undefined,
      dependencies: [{ id: agent.id, hash: agent.hash, kind: "agent" }],
      steps: [
        {
          key: "draft",
          agent_id: agent.id,
          after: [],
          wait_for_confirmation: true,
        },
        {
          key: "review",
          agent_id: agent.id,
          after: ["draft"],
          wait_for_confirmation: false,
        },
      ],
    },
  });
  const settings = await (await page.request.get("/api/models")).json();
  const config = await post(page, "/api/models", {
    provider: "deepseek",
    name: "本地编排协议",
    model: "deepseek-flash",
    base_url: "https://api.deepseek.com",
    api_key: "fixture-only-never-real",
    enabled: true,
  });
  const binding = await page.request.put("/api/model-bindings/writer", {
    headers,
    data: {
      connection_id: config.id,
      connection_revision: 1,
      revision:
        settings.bindings.find((b: { role: string }) => b.role === "writer")
          ?.revision ?? 0,
    },
  });
  expect(binding.status()).toBe(200);
  await post(page, "/api/model-prices", {
    connection_id: config.id,
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
  async function budget(enabled: boolean) {
    const current = await (await page.request.get("/api/model-budget")).json();
    const response = await page.request.put("/api/model-budget", {
      headers,
      data: {
        ...current,
        enabled,
        limit_micros: current.spent_micros + current.reserved_micros + 10000000,
        per_call_micros: 1000000,
      },
    });
    expect(response.status()).toBe(200);
  }
  async function open() {
    await page
      .getByRole("button", { name: "Skill 研究与编辑", exact: true })
      .click();
    await page
      .locator(".capability-panel")
      .getByRole("button", {
        name: new RegExp(
          `浏览器完整工作流 · 工作流 · ${recipe.id.slice(0, 8)}`,
        ),
      })
      .click();
    await expect(
      page.getByTestId("recipe-execution").getByLabel("执行分支"),
    ).toHaveValue(branch.id);
  }
  try {
    await budget(false);
    await open();
    const panel = page.getByTestId("recipe-execution");
    await panel.getByLabel("本次为显式试运行，不作为正式质量资格").check();
    await panel
      .getByLabel("本次工作流资料 · 资料", { exact: true })
      .fill("整体目标不得自动继承");
    await panel.getByLabel("整个任务的费用上限").fill("1");
    await panel
      .getByLabel("步骤 draft 资料 · 资料", { exact: true })
      .fill("第一步独立材料🌙");
    await panel
      .getByLabel("步骤 review 资料 · 资料", { exact: true })
      .fill("交接占位，不应进入最终结果");
    const second = panel.getByRole("group", {
      name: "执行步骤 review",
      exact: true,
    });
    await second.getByText("指定交接内容与资料包", { exact: true }).click();
    await second.getByRole("button", { name: "增加交接内容" }).click();
    await second.getByLabel("读取内容路径", { exact: true }).fill("/missing");
    await page.reload();
    await page.getByRole("button", { name, exact: true }).click();
    await open();
    await expect(
      panel.getByLabel("步骤 draft 资料 · 资料", { exact: true }),
    ).toHaveValue("第一步独立材料🌙");
    const prepared = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith("/recipe-executions"),
    );
    await panel
      .getByRole("button", { name: "保存并准备工作流", exact: true })
      .click();
    const run = await (await prepared).json();
    expect(run.state).toBe("PREPARED");
    await panel
      .getByRole("button", { name: "按任务上限启动", exact: true })
      .click();
    await expect(panel.getByRole("alert")).toBeVisible();
    expect(
      (
        await (
          await page.request.get(`${root}/recipe-executions/${run.id}`)
        ).json()
      ).spent_micros,
    ).toBe(0);
    await budget(true);
    await panel
      .getByRole("button", { name: "按任务上限启动", exact: true })
      .click();
    const result = panel.getByTestId("recipe-run"),
      draft = result.locator('[data-step="draft"]');
    await expect(
      draft.getByRole("button", { name: "确认此步骤并继续" }),
    ).toBeVisible({ timeout: 60000 });
    const before = await (
      await page.request.get(`${root}/recipe-executions/${run.id}`)
    ).json();
    expect(before.steps[1].generation_id).toBe("");
    await result
      .getByRole("button", { name: "暂停工作流", exact: true })
      .click();
    await expect(
      draft.getByRole("button", { name: "确认此步骤并继续" }),
    ).toBeDisabled();
    await page.reload();
    await page.getByRole("button", { name, exact: true }).click();
    await open();
    await expect(
      result.getByRole("heading", { name: "已暂停", exact: true }),
    ).toBeVisible();
    await result
      .getByRole("button", { name: "继续工作流", exact: true })
      .click();
    await draft.getByRole("button", { name: "确认此步骤并继续" }).click();
    await expect(
      result.getByRole("heading", { name: "等待条件恢复", exact: true }),
    ).toBeVisible({ timeout: 60000 });
    await result
      .getByRole("button", { name: "修改资料并重做 review", exact: true })
      .click();
    await expect(
      result.getByLabel("重做读取内容路径", { exact: true }),
    ).toHaveValue("/missing");
    await result
      .getByLabel("重做读取内容路径", { exact: true })
      .fill("/summary");
    await result
      .getByRole("button", { name: "保存修改并重做此步骤", exact: true })
      .click();
    await expect(
      result.getByRole("heading", { name: "已完成", exact: true }),
    ).toBeVisible({ timeout: 60000 });
    await expect(result.locator('[data-step="review"] pre')).toContainText(
      "第一步独立材料🌙",
    );
    await expect(result.locator('[data-step="review"] pre')).not.toContainText(
      "交接占位",
    );
    const finished = await (
      await page.request.get(`${root}/recipe-executions/${run.id}`)
    ).json();
    expect(finished.spent_micros).toBeGreaterThan(0);
    expect(finished.held_micros).toBe(0);
    await result
      .getByRole("button", { name: "修改资料并重做 draft", exact: true })
      .click();
    await result
      .getByLabel("重做资料 · 资料", { exact: true })
      .fill("作者修改后的独立资料🌙");
    await page.reload();
    await page.getByRole("button", { name, exact: true }).click();
    await open();
    await expect(
      result.getByLabel("重做资料 · 资料", { exact: true }),
    ).toHaveValue("作者修改后的独立资料🌙");
    await expect(
      result.getByLabel("重做输出上限", { exact: true }),
    ).toHaveValue("1024");
    await result.getByLabel("重做输出上限", { exact: true }).fill("2048");
    await result
      .getByRole("button", { name: "保存修改并重做此步骤", exact: true })
      .click();
    await expect(
      draft.getByRole("button", { name: "确认此步骤并继续" }),
    ).toBeVisible({ timeout: 60000 });
    await expect(draft).toContainText("第 2 次");
    await post(
      page,
      `${root}/recipe-executions/${run.id}/confirm`,
      {
        expected_revision: before.revision,
        step_key: "draft",
        output_hash: before.steps[0].output_hash,
      },
      409,
    );
    const retried = await (
      await page.request.get(`${root}/recipe-executions/${run.id}`)
    ).json();
    expect(retried.spent_micros).toBeGreaterThan(finished.spent_micros);
    expect(retried.steps).toHaveLength(5);
    await result.getByText("查看 3 条历史尝试", { exact: true }).click();
    await expect(
      result.getByText("第一步独立材料🌙", { exact: false }).first(),
    ).toBeVisible();
    await page.screenshot({
      path: "../.runtime/recipe-execution.png",
      fullPage: true,
    });
    await result
      .getByRole("button", { name: "取消工作流", exact: true })
      .click();
    await expect(
      result.getByRole("heading", { name: "已取消", exact: true }),
    ).toBeVisible();
    const branches = await (await page.request.get(`${root}/branches`)).json();
    expect(
      branches.items.find((b: { id: string }) => b.id === branch.id).head,
    ).toBe("");
  } finally {
    await budget(false);
  }
});
