import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";

const chapter = "1".repeat(32),
  control = "2".repeat(32),
  body = "3".repeat(32),
  ledger = "4".repeat(32),
  world = "5".repeat(32),
  head = "a".repeat(32),
  planID = "6".repeat(32),
  runID = "7".repeat(32),
  projectID = "8".repeat(32),
  branchID = "9".repeat(32);
async function fixture(page: Page, info: TestInfo) {
  const path = "/src/features/novel/NovelWorkspace.vue",
    vite = new URL(info.project.use.baseURL!).origin;
  const compiled = await (await page.request.get(path)).text(),
    vue = compiled.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const setup = {
    title: "雾城",
    genre: "悬疑",
    language: "简体中文",
    source_mode: "idea",
    source_text: "",
    ip_mode: "existing",
    project_id: projectID,
    branch_id: branchID,
    base_head: head,
    idea: "旧案",
    conflict: "旧约",
    audience: "",
    world: "旧城",
    theme: "守约",
    include: "",
    avoid: "",
    perspective: "第三人称",
    style: "克制",
    target_words: 300000,
    volume_count: 3,
    chapter_min: 2000,
    chapter_max: 4000,
    character_mode: "manual",
    characters: [],
    creation_mode: "manual",
    model_choice: "later",
    connection_id: "",
    protection: "strict",
  };
  const book = {
    id: "book",
    project_id: projectID,
    branch_id: branchID,
    settings_revision: 1,
    stage: "WORLD_PLANNING",
    setup,
  };
  const snapshot = {
    entities: [],
    assertions: [],
    rules: [],
    events: [],
    mappings: [],
  };
  const branch = { id: branchID, name: "正史", head, snapshot, revision: 1 };
  const resource = (id: string, kind: string, name: string) => ({
    id,
    project_id: projectID,
    branch_id: branchID,
    kind,
    name,
    head,
    hash: "c".repeat(64),
    revision: 1,
  });
  const resources = [
    resource(control, "novel_chapter_control", `章节工作稿 · ${chapter}`),
    resource(body, "novel_chapter_body", `章节正文 · ${chapter}`),
    resource(ledger, "novel_chapter_ledger", `剧情账本 · ${chapter}`),
    resource(world, "novel_plan_world", "世界与故事"),
  ];
  const reference = (id: string) => ({
    kind: "resource",
    branch_id: branchID,
    id,
    revision: head,
    hash: "combined-dependency-hash",
  });
  const changePlan = {
    id: planID,
    branch_id: branchID,
    stale: false,
    semantic_review: "NOT_RUN",
    source_event_id: "event",
    source_state: "CURRENT",
    changes: [],
    differences: [],
    items: [body, world, ledger].map((id) => ({
      node: reference(id),
      action: "REVIEW",
      via: [],
    })),
    untracked: [],
    outside_recorded_closure: [],
  };
  const counts = {
    reads: [] as string[],
    writes: [] as string[],
    previews: 0,
    confirms: 0,
  };
  let mode: "normal" | "hold" | "deny" | "wrongBody" | "holdRecheck" = "normal",
    text = "🌙旧约未尽。",
    revision = 0;
  const held: (() => void)[] = [];
  const server = createServer((request, response) => {
    const url = new URL(request.url!, "http://127.0.0.1"),
      json = (value: unknown, status = 200) =>
        response
          .writeHead(status, { "Content-Type": "application/json" })
          .end(JSON.stringify(value));
    if (url.pathname === "/")
      return response
        .writeHead(200, { "Content-Type": "text/html; charset=utf-8" })
        .end(
          `<!doctype html><meta charset="utf-8"><div id="app"></div><script type="module">import{createApp,h}from ${JSON.stringify(new URL(vue!, vite).href)};import Component from ${JSON.stringify(vite + path)};createApp({render:()=>h(Component,{book:${JSON.stringify(book)},project:{id:${JSON.stringify(projectID)},name:'雾城',role:'owner',revision:1},actor:{id:'owner',workspace_id:'workspace',username:'owner',role:'owner'}})}).mount('#app');</script>`,
        );
    let raw = "";
    request.on("data", (chunk) => (raw += chunk));
    request.on("end", () => {
      const input = raw ? JSON.parse(raw) : {};
      counts.reads.push(`${request.method} ${url.pathname}`);
      if (
        request.method !== "GET" &&
        !url.pathname.endsWith("/settings/preview")
      )
        counts.writes.push(url.pathname);
      if (url.pathname.endsWith("/settings/draft"))
        return json({ revision: 1, base_revision: 1, setup });
      if (url.pathname.endsWith("/settings/versions"))
        return json({ items: [{ revision: 1, setup }] });
      if (url.pathname.endsWith("/settings/preview")) {
        counts.previews++;
        return json({
          hash: `preview-${counts.previews}`,
          draft_revision: 1,
          base_revision: 1,
          planning_changed: true,
          changes: [
            {
              field: "world",
              label: "世界背景",
              scope: "world",
              before: "旧城",
              after: "新城",
            },
          ],
          review_resources: resources
            .filter((r) => r.id !== control)
            .map(({ id, name, head }) => ({ id, name, head })),
        });
      }
      if (url.pathname.endsWith("/settings/confirm")) {
        counts.confirms++;
        return json(book);
      }
      if (url.pathname.endsWith("/plans"))
        return json({
          stage: "WORLD_PLANNING",
          resources: resources.filter((r) => r.kind.startsWith("novel_plan_")),
          sources: [],
        });
      if (url.pathname.endsWith(`/plans/${world}/draft`))
        return json({
          resource: resources[3],
          revision: 0,
          base_head: head,
          hash: "world-hash",
          ready: false,
          reason: "世界依据需要核对",
          document: {
            schema: 1,
            kind: "world",
            scope: "",
            settings_revision: 1,
            settings_hash: "settings",
            ip_head: head,
            inputs: [],
            title: "世界与故事规划",
            summary: "保留的世界草稿",
            mainline: "查明旧案",
            theme: "守约",
            world_rules: "规则",
            costs: "",
            boundaries: "",
            setting: "旧城",
            factions: "",
            characters: "",
            arcs: "",
            turning_points: "",
            ending: "",
            open_questions: "",
            volumes: [],
            chapters: [],
            scenes: [],
          },
        });
      if (url.pathname.endsWith(`/chapters/${control}/draft`)) {
        if (request.method === "PUT") {
          text = input.document.text;
          revision++;
        }
        return json({
          resource: resources[0],
          revision,
          base_head: head,
          hash: revision ? "new-draft" : "draft",
          ready: true,
          reason: "",
          document: {
            schema: 1,
            chapter_id: chapter,
            body_id: mode === "wrongBody" ? "f".repeat(32) : body,
            detail: reference(world),
            settings_revision: 1,
            settings_hash: "settings",
            ip_head: head,
            story_time: 7,
            perspective: "",
            title: "旧约",
            text,
            references: [],
          },
        });
      }
      if (url.pathname.endsWith(`/chapters/${control}/ledger`))
        return json({
          resource: resources[2],
          revision: 0,
          base_head: head,
          hash: "ledger-hash",
          ready: false,
          reason: "原依据待核对",
          document: {
            schema: 1,
            chapter_id: chapter,
            body_id: body,
            source: {
              book_id: "book",
              resource_id: control,
              revision: 0,
              base_head: head,
              hash: "draft",
            },
            text_hash: "text-hash",
            summary: "原账本摘要",
            entries: [],
            notes: [],
          },
        });
      if (url.pathname.endsWith("/release")) return json(null);
      if (url.pathname.endsWith("/export/preview"))
        return json({
          hash: "p",
          title: "雾城",
          source: input.source,
          chapters: [],
          included: 0,
          missing: 0,
          warnings: 0,
          characters: 0,
        });
      if (url.pathname.endsWith("/writing-batches/chapters"))
        return json({ items: [] });
      if (url.pathname.endsWith("/branches")) return json({ items: [branch] });
      if (url.pathname.endsWith(`/branches/${branchID}`)) return json(branch);
      if (url.pathname.endsWith(`/branches/${branchID}/draft`))
        return json({
          revision: 1,
          base_head: head,
          snapshot,
          amendment: false,
          amendment_reason: "",
          change_reasons: {},
          branch_actions: {},
        });
      if (url.pathname.endsWith("/resources")) {
        const deliver = () => json({ items: resources });
        if (mode === "hold") {
          held.push(deliver);
          return;
        }
        if (mode === "deny") return json({ error: "当前权限已撤回" }, 403);
        return deliver();
      }
      if (url.pathname.endsWith("/dependencies/catalog"))
        return json({
          items: resources.map((r) => ({
            ref: reference(r.id),
            name: r.name,
            branch_name: "正史",
          })),
        });
      if (url.pathname.endsWith("/changes"))
        return json({
          items: [
            {
              id: planID,
              release_id: "",
              changes: 1,
              impacts: 3,
              source_event_id: "event",
              created_at: "当前记录",
            },
          ],
        });
      if (url.pathname.endsWith(`/changes/${planID}`)) {
        if (mode === "holdRecheck") {
          held.push(() => json(changePlan));
          return;
        }
        return json(changePlan);
      }
      if (url.pathname.endsWith("/inspection-coverage"))
        return json({
          items: [
            {
              ref: reference(body),
              state: "FINDINGS",
              runs: [runID],
              checked: false,
              pending: false,
            },
          ],
          checked: 0,
          pending: 0,
          findings: 1,
          uncertain: 0,
          needs_attention: 1,
          unreadable_runs: 0,
          outside_scope: 0,
          untracked: 0,
          automatic_ready: false,
          all_listed_checked: false,
          next_batch: [],
        });
      if (url.pathname.endsWith("/inspections"))
        return json({
          items: [
            {
              id: runID,
              state: "SUCCEEDED",
              paused: false,
              cancelled: false,
              frozen: { estimate_micros: 0, price: { currency: "USD" } },
            },
          ],
          next: "",
          runtime_ready: false,
        });
      if (url.pathname.endsWith(`/generations/${runID}`))
        return json({
          id: runID,
          state: "SUCCEEDED",
          paused: false,
          cancelled: false,
          frozen: { estimate_micros: 0, price: { currency: "USD" } },
        });
      if (url.pathname.endsWith("/change-inspection"))
        return json({
          generation_id: runID,
          plan_id: planID,
          state: "SUCCEEDED",
          current: true,
          counted: true,
          reason: "原报告保留",
          scope: "正文旧版本",
          items: [
            {
              ref: reference(body),
              name: "旧约正文",
              state: "FINDINGS",
              issues: [],
            },
          ],
          report: {
            verdict: "FINDINGS",
            summary: "原问题仍需核对",
            issues: [
              {
                target_key: `resource:${body}`,
                severity: "blocker",
                explanation: "称谓与修改后的设定不一致",
                anchor_keys: ["anchor"],
              },
            ],
          },
          anchors: [
            {
              key: "anchor",
              target_key: `resource:${body}`,
              text: "原证据JSON，不是可直接选择的正文引用",
            },
          ],
        });
      if (url.pathname.endsWith("/outcomes"))
        return json({ items: [], next: "", notes: [] });
      return json({ items: [], ready: false, enabled: false });
    });
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  await page.clock.install({ time: new Date("2026-10-29T14:00:00Z") });
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  return {
    counts,
    setMode: (value: typeof mode) => (mode = value),
    release: () => held.splice(0).forEach((fn) => fn()),
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}
async function settings(page: Page) {
  await page
    .getByRole("navigation", { name: "本书工作区" })
    .getByRole("button", { name: "故事规划", exact: true })
    .click();
  await page
    .getByRole("button", { name: "修改小说设置与查看历史", exact: true })
    .click();
  await page.getByRole("button", { name: "查看修改差异", exact: true }).click();
}
async function impacts(page: Page) {
  await page
    .getByRole("navigation", { name: "本书工作区" })
    .getByRole("button", { name: "人物与资料", exact: true })
    .click();
  await page
    .getByRole("button", { name: "编辑本书世界与角色", exact: true })
    .click();
  await page
    .getByRole("navigation", { name: "设定库工作分区" })
    .getByRole("button", { name: "版本与分支", exact: true })
    .click();
  await page.getByText("依赖、冻结与变更影响", { exact: true }).click();
  await page.getByLabel("已保存的影响清单").selectOption(planID);
  await page
    .getByRole("button", { name: "打开并检查清单时效", exact: true })
    .click();
}

test("设置差异直达正文与规划，纯回查零写返回原列表，旧确认须重新预览", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    await settings(page);
    await page
      .getByRole("checkbox", { name: "我已查看差异，确认采用这些设置" })
      .check();
    await page
      .getByRole("button", { name: `打开小说资料 ${body}`, exact: true })
      .click();
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      "🌙旧约未尽。",
    );
    await page
      .getByRole("button", { name: "保存并返回设置差异", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "设置修改差异" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "确认保存新版本", exact: true }),
    ).toBeDisabled();
    await page
      .getByRole("button", { name: `打开小说资料 ${world}`, exact: true })
      .click();
    await expect(page.getByLabel("内容摘要", { exact: true })).toHaveValue(
      "保留的世界草稿",
    );
    await page
      .getByRole("button", { name: "保存并返回设置差异", exact: true })
      .click();
    expect(f.counts.writes).toHaveLength(0);
    expect(f.counts.confirms).toBe(0);
    expect(f.counts.previews).toBe(1);
    await page
      .getByRole("button", { name: "查看修改差异", exact: true })
      .click();
    await page
      .getByRole("checkbox", { name: "我已查看差异，确认采用这些设置" })
      .check();
    await expect(
      page.getByRole("button", { name: "确认保存新版本", exact: true }),
    ).toBeEnabled();
    expect(f.counts.previews).toBe(2);
  } finally {
    await f.close();
  }
});

test("独立变更问题回正文后保留原报告与焦点，GET重查不当作重审", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    await impacts(page);
    const inspection = page.getByRole("region", {
      name: "独立变更影响审查",
      exact: true,
    });
    await inspection
      .getByRole("button", { name: new RegExp(`执行完成 · ${runID}`) })
      .click();
    const report = inspection.getByRole("region", {
      name: "本批审查结果",
      exact: true,
    });
    const issue = report
      .locator("article")
      .filter({ hasText: "称谓与修改后的设定不一致" });
    await issue
      .getByRole("button", { name: `打开小说资料 ${body}`, exact: true })
      .click();
    await expect(page.getByLabel("章节正文", { exact: true })).toBeVisible();
    await page
      .getByRole("button", { name: "保存并返回变更清单", exact: true })
      .click();
    await expect(report).toBeVisible();
    await expect(report).toContainText("原问题仍需核对");
    await expect(
      issue.getByRole("button", { name: `打开小说资料 ${body}`, exact: true }),
    ).toBeFocused();
    expect(
      f.counts.reads.filter((r) => r.endsWith("/change-inspection")).length,
    ).toBeGreaterThan(1);
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("慢资料读取期间新修改保留并阻止迟到跳转", async ({ page }, info) => {
  const f = await fixture(page, info);
  try {
    await settings(page);
    f.setMode("hold");
    await page
      .getByRole("button", { name: `打开小说资料 ${body}`, exact: true })
      .click();
    await expect
      .poll(() => f.counts.reads.filter((r) => r.endsWith("/resources")).length)
      .toBe(1);
    await page.getByLabel("世界背景", { exact: true }).fill("读取期间的新输入");
    f.setMode("normal");
    f.release();
    await expect(page.getByLabel("世界背景", { exact: true })).toHaveValue(
      "读取期间的新输入",
    );
    await expect(
      page.getByRole("region", { name: "修订导航" }).getByRole("alert"),
    ).toContainText("原页面");
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveCount(0);
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("返回后的原清单重查超时仍可离开，不取消或重发写请求", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    await impacts(page);
    await page
      .getByTestId("impact-plan")
      .getByRole("button", { name: `打开小说资料 ${body}`, exact: true })
      .click();
    await expect(page.getByLabel("章节正文", { exact: true })).toBeVisible();
    f.setMode("holdRecheck");
    await page
      .getByRole("button", { name: "保存并返回变更清单", exact: true })
      .click();
    await expect
      .poll(
        () =>
          f.counts.reads.filter((r) => r.endsWith(`/changes/${planID}`)).length,
      )
      .toBeGreaterThan(1);
    await page.clock.fastForward(16000);
    await expect(
      page
        .getByTestId("dependency-panel")
        .getByRole("alert")
        .filter({ hasText: "超时" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "返回本书工作区", exact: true }),
    ).toBeEnabled();
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("变更影响经既有层级打开正文，手改返回保留清单并只读重查", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    await impacts(page);
    const panel = page.getByTestId("impact-plan");
    await panel
      .getByRole("button", { name: `打开小说资料 ${body}`, exact: true })
      .click();
    await page
      .getByLabel("章节正文", { exact: true })
      .fill("🌙已修订称谓的正文。");
    await page
      .getByRole("button", { name: "保存并返回变更清单", exact: true })
      .click();
    await expect(panel).toBeVisible();
    await expect(page.getByLabel("已保存的影响清单")).toHaveValue(planID);
    await expect(
      page.getByText("私人修改", { exact: false }).first(),
    ).toBeVisible();
    expect(
      f.counts.reads.filter((r) => r.endsWith(`/changes/${planID}`)).length,
    ).toBeGreaterThan(1);
    expect(f.counts.writes).toEqual([
      `/api/novels/book/chapters/${control}/draft`,
    ]);
  } finally {
    await f.close();
  }
});

test("账本回查不新建资料，读取失败留原设置输入且可重试，错误body映射拒绝", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    await settings(page);
    f.setMode("deny");
    await page
      .getByRole("button", { name: `打开小说资料 ${ledger}`, exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText("撤回");
    await expect(
      page.getByRole("region", { name: "设置修改差异" }),
    ).toBeVisible();
    f.setMode("normal");
    await page
      .getByRole("button", { name: `打开小说资料 ${ledger}`, exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "本章剧情账本" }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "保存账本并返回修订清单", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "设置修改差异" }),
    ).toBeVisible();
    f.setMode("wrongBody");
    await page
      .getByRole("button", { name: `打开小说资料 ${body}`, exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText("不一致");
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});
