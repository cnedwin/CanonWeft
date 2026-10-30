import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";
import { openIPSection } from "./ip-edit-helper";

test("发布后自动登记真实影响，刷新查看不改写资料", async ({ page }) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  async function post(path: string, data: unknown, expected = 201) {
    const response = await page.request.post(path, { headers, data });
    expect(response.status(), await response.text()).toBe(expected);
    return response.json();
  }
  const name = `自动影响 ${Date.now()}`;
  const project = await post("/api/projects", {
    name,
    operation_id: crypto.randomUUID(),
  });
  const root = `/api/projects/${project.id}`;
  const branch = await post(`${root}/branches`, {
    name: "正史",
    operation_id: crypto.randomUUID(),
  });
  const resource = await post(`${root}/resources`, {
    name: "背景资料",
    branch_id: branch.id,
    kind: "note",
    operation_id: crypto.randomUUID(),
  });
  const release = await post(`${root}/releases`, {
    branch_id: branch.id,
    expected_ip_head: "",
    operation_id: crypto.randomUUID(),
    updates: [
      {
        resource_id: resource.id,
        expected_head: "",
        text: "这是由作者确认的完整资料。",
      },
    ],
  });
  await post(`${root}/releases/${release.id}/adopt`, {}, 200);
  await post(`${root}/releases/${release.id}/publish`, {}, 200);
  let savedID = "";
  await expect
    .poll(
      async () => {
        const response = await page.request.get(
          `${root}/changes?branch=${branch.id}`,
        );
        expect(response.status()).toBe(200);
        const item = (await response.json()).items.find(
          (p: { release_id: string; source_event_id?: string }) =>
            p.release_id === release.id && p.source_event_id,
        );
        savedID = item?.id ?? "";
        return savedID;
      },
      { timeout: 30000 },
    )
    .not.toBe("");
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("依赖、冻结与变更影响", { exact: true }).click();
  await page
    .getByRole("button", { name: "刷新发布后的影响记录", exact: true })
    .click();
  await page
    .getByLabel("已保存的影响清单", { exact: true })
    .selectOption(savedID);
  await page
    .getByRole("button", { name: "打开并检查清单时效", exact: true })
    .click();
  const plan = page.getByTestId("impact-plan");
  await expect(plan).toContainText("这是发布后自动保存的影响记录");
  await expect(plan).toContainText("这是由作者确认的完整资料。");
  await expect(plan).toContainText("独立审查按所选批次记录");
  const current = await (
    await page.request.get(`${root}/resources/${resource.id}`)
  ).json();
  expect(current.head).toBe(release.id);
  expect(current.text).toBe("这是由作者确认的完整资料。");
});

test("处理记录区分候选与当前发布，刷新后可安全切到受影响分支（受控 UI）", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  async function post(path: string, data: unknown) {
    const response = await page.request.post(path, { headers, data });
    expect(response.status()).toBe(201);
    return response.json();
  }
  const name = `跨分支处理 ${Date.now()}`;
  const project = await post("/api/projects", {
    name,
    operation_id: crypto.randomUUID(),
  });
  const root = `/api/projects/${project.id}`;
  const source = await post(`${root}/branches`, {
    name: "来源分支",
    operation_id: crypto.randomUUID(),
  });
  const target = await post(`${root}/branches`, {
    name: "受影响分支",
    operation_id: crypto.randomUUID(),
  });
  let id = "a".repeat(32);
  const plan = () => ({
    id,
    branch_id: source.id,
    source_event_id: "event",
    source_state: "CURRENT",
    stale: false,
    semantic_review: "NOT_RUN",
    changes: [],
    differences: [],
    items: [
      {
        node: {
          kind: "resource",
          id: "resource",
          branch_id: target.id,
          revision: "version",
          hash: "b".repeat(64),
        },
        action: "REVIEW",
        via: [],
      },
    ],
    untracked: [],
    outside_recorded_closure: [],
  });
  await page.route(`**${root}/changes?branch=${source.id}`, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id,
            release_id: "",
            changes: 1,
            impacts: 1,
            created_at: "2026-10-26",
            source_event_id: "event",
          },
        ],
      },
    }),
  );
  await page.route(`**${root}/changes/*`, (route) =>
    route.fulfill({ json: plan() }),
  );
  await page.route(`**${root}/changes/*/refresh`, (route) => {
    id = "c".repeat(32);
    return route.fulfill({ status: 201, json: plan() });
  });
  let nextAttempts = 0;
  await page.route(`**${root}/changes/*/outcomes*`, (route) => {
    const more = new URL(route.request().url()).searchParams.has("after");
    if (more && ++nextAttempts === 1)
      return route.fulfill({
        status: 503,
        json: { error: "分页读取暂不可用" },
      });
    return route.fulfill({
      json: {
        items: [
          {
            resource_id: "r1",
            name: "待复核章节",
            branch_id: target.id,
            release_id: "candidate",
            status: "PREPARED",
            current: false,
            reason: "候选里的理由",
          },
          {
            resource_id: "r2",
            name: "历史路线资料",
            branch_id: target.id,
            release_id: "published",
            status: "PUBLISHED",
            current: true,
            reason: "保留已确认的历史引用",
          },
        ].slice(more ? 1 : 0, more ? 2 : 1),
        truncated: !more,
        next: more ? undefined : "e".repeat(32),
        plan_stale: false,
        untracked: 1,
        outside: 2,
      },
    });
  });
  await page.route(`**${root}/generation-options`, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            role: "continuity_editor",
            name: "本地界面夹具",
            model: "fixture",
            price: { id: "price", currency: "USD" },
          },
        ],
      },
    }),
  );
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "来源分支", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("依赖、冻结与变更影响", { exact: true }).click();
  await page.getByLabel("已保存的影响清单", { exact: true }).selectOption(id);
  await page
    .getByRole("button", { name: "打开并检查清单时效", exact: true })
    .click();
  await page.getByRole("button", { name: "查看处理记录", exact: true }).click();
  const outcomes = page.getByRole("region", { name: "版本绑定的处理记录" });
  await expect(outcomes).toContainText("候选已保存，尚待复核与采用");
  await outcomes
    .getByRole("button", { name: "继续加载处理记录", exact: true })
    .click();
  await expect(
    page.getByText("分页读取暂不可用", { exact: true }),
  ).toBeVisible();
  await expect(outcomes).toContainText("候选已保存，尚待复核与采用");
  await outcomes
    .getByRole("button", { name: "继续加载处理记录", exact: true })
    .click();
  await expect(outcomes).toContainText("已发布，仍是当前版本");
  await expect(
    outcomes.getByRole("button", { name: "继续加载处理记录", exact: true }),
  ).toHaveCount(0);
  await expect(outcomes).toContainText("原记录范围外 2 项");
  await page
    .getByRole("button", { name: "按当前目标版本重新检查", exact: true })
    .click();
  await expect(
    page.getByLabel("已保存的影响清单", { exact: true }),
  ).toHaveValue("c".repeat(32));
  const generation = page.locator(".generation-panel");
  await generation.getByLabel("希望 AI 协助的内容").fill("不能丢失的想法");
  await page
    .getByRole("button", { name: "到此内容所属分支处理", exact: true })
    .click();
  await expect(
    page.getByText("请先保存或放弃当前未完成的编辑，再切换分支处理。", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(generation.getByLabel("希望 AI 协助的内容")).toHaveValue(
    "不能丢失的想法",
  );
  await generation
    .getByRole("button", { name: "清空输入", exact: true })
    .click();
  const switched = page.waitForResponse((r) =>
    r.url().endsWith(`${root}/branches/${target.id}`),
  );
  await page
    .getByRole("button", { name: "到此内容所属分支处理", exact: true })
    .click();
  await switched;
  await expect(page.locator(".generation-panel")).toContainText(
    "已关联变更影响清单",
  );
});
