import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";
import { planFields, sceneFields } from "../src/features/novel/planningTypes";

const originalText = "雨打窗棂，林舟收起了那封信。";
const resource = {
  id: "body",
  name: "第一章",
  kind: "novel_chapter_control",
  head: "",
  revision: 1,
};
const pin = (revision: string) => ({
  kind: "resource",
  branch_id: "branch",
  id: "detail",
  revision,
  hash: revision,
});
function detail(summary: string) {
  return {
    schema: 1,
    kind: "detail",
    scope: "chapter",
    settings_revision: 1,
    settings_hash: "settings",
    ip_head: "old-ip",
    inputs: [],
    ...Object.fromEntries(planFields.map((field) => [field.key, ""])),
    title: "第一章细纲",
    summary,
    volumes: null,
    chapters: null,
    scenes: [
      {
        ...Object.fromEntries(sceneFields.map((field) => [field.key, ""])),
        id: "scene",
        goal: summary,
        words: 2000,
      },
    ],
  };
}
async function fixture(
  page: Page,
  info: TestInfo,
  kind: "workspace" | "review" | "editor",
) {
  const path = `/src/features/novel/${kind === "workspace" ? "NovelWorkspace" : kind === "review" ? "NovelSourceReview" : "NovelChapterEditor"}.vue`;
  const compiled = await (await page.request.get(path)).text();
  const vue = compiled.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  const counts = {
    review: 0,
    preview: 0,
    basis: 0,
    saves: 0,
    planReads: [] as string[],
    unexpectedWrites: [] as string[],
    basisBodies: [] as unknown[],
    texts: [] as string[],
    closed: 0,
  };
  let mode:
    | "normal"
    | "failure"
    | "denied"
    | "hold"
    | "mismatch"
    | "conflict"
    | "write-hold" = "normal";
  let draft = {
    resource,
    revision: 1,
    base_head: "",
    hash: "draft-1",
    ready: false,
    reason: "细纲已经变化，请复核",
    document: {
      schema: 1,
      chapter_id: "chapter",
      body_id: "text-body",
      detail: pin("old"),
      settings_revision: 1,
      settings_hash: "settings",
      ip_head: "old-ip",
      story_time: 1,
      perspective: "",
      title: "第一章",
      text: originalText,
      references: [],
    },
  };
  const held: (() => void)[] = [];
  const server = createServer((request, response) => {
    const url = new URL(request.url!, "http://127.0.0.1");
    const json = (value: unknown, status = 200) =>
      response
        .writeHead(status, { "Content-Type": "application/json" })
        .end(JSON.stringify(value));
    if (url.pathname === "/")
      return response.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
      })
        .end(`<!doctype html><meta charset="utf-8"><button id="leave">离开测试页</button><div id="app"></div><script type="module">
import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};
import Component from ${JSON.stringify(vite + path)};
const book={id:'book',project_id:'project',branch_id:'branch',settings_revision:1,stage:'WORLD_PLANNING',setup:{title:'雾城',genre:'悬疑',language:'简体中文',idea:'调查旧案',conflict:'真相与承诺',target_words:300000,volume_count:1,chapter_min:2000,chapter_max:4000,characters:[]}};
window.expiredEvents=0;const app=createApp({render:()=>h(Component,{book,bookId:'book',title:'雾城',chapterId:'chapter',project:{id:'project',name:'雾城',role:'owner',revision:1},actor:{id:'owner',workspace_id:'workspace',username:'owner',role:'owner'},onExpired:()=>window.expiredEvents++})});
app.mount('#app');document.querySelector('#leave').onclick=()=>app.unmount();</script>`);
    let body = "";
    request.on("data", (chunk) => {
      body += chunk;
    });
    request.on("end", () => {
      const input = body ? JSON.parse(body) : {};
      if (url.pathname.endsWith("/source-review")) {
        counts.review++;
        if (mode === "failure") return json({ error: "临时读取失败" }, 503);
        if (mode === "denied") return json({ error: "当前账号无权读取" }, 403);
        if (mode === "hold") {
          held.push(() => json({ items: [], scanned: 0 }));
          response.on("close", () => counts.closed++);
          return;
        }
        return json(
          url.searchParams.has("after")
            ? {
                items: [
                  {
                    resource_id: "material",
                    name: "已撤回地图",
                    kind: "material",
                    status: "withdrawn",
                    reasons: ["地图已撤回"],
                  },
                ],
                scanned: 1,
              }
            : {
                items: [
                  {
                    resource_id: "detail",
                    name: "第一章细纲",
                    kind: "planning",
                    status: "review",
                    reasons: ["人物或世界设定已变化"],
                  },
                  {
                    resource_id: "body",
                    name: "第一章",
                    kind: "chapter",
                    status: "review",
                    chapter_id: "chapter",
                    reasons: ["细纲已经变化"],
                  },
                  {
                    resource_id: "research",
                    name: "旧城药材",
                    kind: "research",
                    status: "review",
                    reasons: ["来源已换版"],
                  },
                ],
                next_cursor: "a".repeat(32),
                scanned: 100,
              },
        );
      }
      if (url.pathname === "/api/novels/book/plans")
        return json({
          stage: "DETAIL_PLANNING",
          reason: "请核对细纲",
          resources: [
            {
              id: "detail",
              name: "章节细纲 · chapter",
              kind: "novel_plan_detail",
              head: "detail-head",
              revision: 1,
            },
          ],
          sources: [],
        });
      if (url.pathname === "/api/novels/book/plans/detail/draft") {
        counts.planReads.push("detail");
        return json({
          resource: {
            id: "detail",
            name: "第一章细纲",
            kind: "novel_plan_detail",
            head: "detail-head",
            revision: 1,
          },
          revision: 1,
          base_head: "detail-head",
          hash: "plan-hash",
          document: detail("沿河调查"),
          ready: false,
          reason: "上游已变化",
        });
      }
      if (
        url.pathname.startsWith("/api/novels/book/plans/") &&
        request.method !== "GET"
      ) {
        counts.unexpectedWrites.push(url.pathname);
        return json({ error: "Unexpected planning write" }, 500);
      }
      if (url.pathname === "/api/projects/project/releases")
        return json({ items: [] });
      if (url.pathname.endsWith("/export/preview"))
        return json({
          hash: "preview",
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
      if (
        url.pathname === "/api/novels/book/chapters" &&
        request.method === "POST"
      )
        return json(resource);
      if (url.pathname.endsWith("/basis-preview")) {
        counts.preview++;
        const snapshot = structuredClone(draft);
        const deliver = () =>
          json({
            resource_id: mode === "mismatch" ? "different" : resource.id,
            chapter_id: "chapter",
            revision: snapshot.revision,
            hash: snapshot.hash,
            base_head: "",
            current_head: "",
            basis_hash: `review-${snapshot.revision}`,
            can_refresh: true,
            reason: "核对后更新引用版本",
            before: {
              detail_ref: pin("old"),
              detail: detail("独自走进旧宅"),
              settings_revision: 1,
              settings_hash: "settings",
              ip_head: "old-ip",
            },
            after: {
              detail_ref: pin("new"),
              detail: detail("与同伴沿河调查"),
              settings_revision: 2,
              settings_hash: "new-settings",
              ip_head: "new-ip",
            },
            settings_changes: [
              {
                field: "narrative",
                label: "叙事方式",
                scope: "generation",
                before: "第一人称",
                after: "第三人称",
              },
            ],
            ip_changes: [
              {
                ref: {
                  kind: "entity",
                  branch_id: "branch",
                  id: "person",
                  revision: "new-ip",
                  hash: "person-hash",
                },
                name: "林舟",
                fields: [{ name: "职业", before: "药师", after: "医官" }],
              },
            ],
            limitations: [],
          });
        if (mode === "denied") return json({ error: "无权读取当前章稿" }, 403);
        if (mode === "hold") {
          held.push(deliver);
          response.on("close", () => counts.closed++);
          return;
        }
        return deliver();
      }
      if (url.pathname.endsWith("/basis") && request.method === "POST") {
        counts.basis++;
        counts.basisBodies.push(input);
        if (mode === "conflict")
          return json({ error: "依据已再次变化，请重新核对" }, 409);
        if (mode === "write-hold") {
          held.push(() => json({ error: "请求结果尚未确认" }, 503));
          response.on("close", () => counts.closed++);
          return;
        }
        return json({
          ...draft,
          document: {
            ...draft.document,
            detail: pin("new"),
            settings_revision: 2,
            settings_hash: "new-settings",
            ip_head: "new-ip",
          },
        });
      }
      if (url.pathname.endsWith("/draft")) {
        if (request.method === "PUT") {
          counts.saves++;
          counts.texts.push(input.document.text);
          draft = {
            ...draft,
            document: input.document,
            revision: draft.revision + 1,
            hash: `draft-${draft.revision + 1}`,
          };
        }
        return json(draft);
      }
      if (url.pathname === "/api/projects/project/branches/branch")
        return json({
          id: "branch",
          head: "old-ip",
          snapshot: { entities: [] },
        });
      if (request.method !== "GET") counts.unexpectedWrites.push(url.pathname);
      return json(
        { error: `Unexpected ${request.method} ${url.pathname}` },
        404,
      );
    });
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  await page.clock.install({ time: new Date("2026-10-29T05:00:00Z") });
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  return {
    counts,
    mode: (value: typeof mode) => {
      mode = value;
    },
    release: () => held.splice(0).forEach((fn) => fn()),
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

test("待复核队列支持类型原因查找和继续扫描，暂时失败可重试，权限拒绝清空旧内容", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "review");
  try {
    await expect(page.getByRole("article")).toHaveCount(3);
    await page.getByLabel("待复核类型").selectOption("planning");
    await page.getByRole("searchbox").fill("人物");
    await expect(page.getByRole("article")).toHaveCount(1);
    await page.getByLabel("待复核类型").selectOption("all");
    await page.getByRole("searchbox").fill("");
    await page.getByRole("button", { name: "继续检查更多资料" }).click();
    await expect(page.getByRole("article")).toHaveCount(4);
    state.mode("failure");
    await page.getByRole("button", { name: "重新检查资料状态" }).click();
    await expect(page.getByRole("alert")).toContainText("保留上次清单");
    state.mode("denied");
    await page.getByRole("button", { name: "重新检查资料状态" }).click();
    await expect(page.getByRole("article")).toHaveCount(0);
    state.mode("normal");
    await page.getByRole("button", { name: "重新检查资料状态" }).click();
    await expect(page.getByRole("article")).toHaveCount(3);
    state.mode("hold");
    await page.getByRole("button", { name: "重新检查资料状态" }).click();
    await page.clock.fastForward(15000);
    await expect(page.getByRole("alert")).toContainText("超时");
    state.release();
    expect(state.counts.unexpectedWrites).toEqual([]);
  } finally {
    await state.close();
  }
});

test("从待复核规划直达对应细纲，保存返回清单并保留筛选；导航不创建新规划", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "workspace");
  try {
    await page.getByText("全部创作工具", { exact: true }).click();
    await page
      .getByRole("button", { name: "资料变更与待复核", exact: true })
      .click();
    await page.getByLabel("待复核类型").selectOption("planning");
    await page.getByRole("searchbox").fill("第一章");
    await page.getByRole("button", { name: "打开这份规划核对" }).click();
    await expect(page.getByLabel("规划标题", { exact: true })).toHaveValue(
      "第一章细纲",
    );
    expect(state.counts.planReads).toEqual(["detail"]);
    await page.getByRole("button", { name: "保存并返回待复核清单" }).click();
    await expect(page.getByRole("searchbox")).toHaveValue("第一章");
    await expect(page.getByLabel("待复核类型")).toHaveValue("planning");
    await expect.poll(() => state.counts.review).toBe(2);
    expect(state.counts.unexpectedWrites).toEqual([]);
  } finally {
    await state.close();
  }
});

test("依据对照展示细纲设置与人物变化，确认才更新引用并保存原文；手改使旧对照失效", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "editor");
  try {
    const text = page.getByLabel("章节正文", { exact: true });
    await expect(text).toHaveValue(originalText);
    await page
      .getByRole("button", { name: "更新写作依据，保留正文", exact: true })
      .click();
    const comparison = page.getByRole("region", { name: "写作依据变更对照" });
    await expect(comparison).toContainText("与同伴沿河调查");
    await expect(comparison).toContainText("医官");
    await expect(comparison).toContainText("第三人称");
    expect(state.counts.basis).toBe(0);
    await text.fill("作者刚刚补入的句子。");
    await expect(
      comparison.getByRole("button", { name: "确认更新依据，正文保持原样" }),
    ).toHaveCount(0);
    await comparison.getByRole("button", { name: "重新读取依据对照" }).click();
    await expect(
      comparison.getByRole("button", { name: "确认更新依据，正文保持原样" }),
    ).toBeEnabled();
    await comparison
      .getByRole("button", { name: "确认更新依据，正文保持原样" })
      .click();
    await expect(comparison).toHaveCount(0);
    await expect(text).toHaveValue("作者刚刚补入的句子。");
    expect(state.counts.basis).toBe(1);
    expect(state.counts.basisBodies).toEqual([
      { revision: 2, basis_hash: "review-2" },
    ]);
    expect(state.counts.texts).toEqual([
      "作者刚刚补入的句子。",
      "作者刚刚补入的句子。",
    ]);
    expect(state.counts.unexpectedWrites).toEqual([]);
  } finally {
    await state.close();
  }
});

test("迟到对照不能覆盖手改，上游换版409需重查；只读超时不取消或重发依据写请求", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "editor");
  try {
    const text = page.getByLabel("章节正文", { exact: true });
    await expect(text).toHaveValue(originalText);
    state.mode("hold");
    await page
      .getByRole("button", { name: "更新写作依据，保留正文", exact: true })
      .click();
    await expect.poll(() => state.counts.preview).toBe(1);
    await text.fill("保留本次手改。");
    state.release();
    const comparison = page.getByRole("region", { name: "写作依据变更对照" });
    await expect(
      comparison.getByRole("button", { name: "确认更新依据，正文保持原样" }),
    ).toHaveCount(0);
    state.mode("denied");
    await comparison.getByRole("button", { name: "重新读取依据对照" }).click();
    await expect(comparison.getByRole("alert")).toContainText("无权");
    await expect(comparison).not.toContainText("医官");
    state.mode("normal");
    await comparison.getByRole("button", { name: "重新读取依据对照" }).click();
    await expect(
      comparison.getByRole("button", { name: "确认更新依据，正文保持原样" }),
    ).toBeEnabled();
    state.mode("conflict");
    await comparison
      .getByRole("button", { name: "确认更新依据，正文保持原样" })
      .click();
    await expect(
      page.getByRole("alert").filter({ hasText: "依据已再次变化" }),
    ).toBeVisible();
    await expect(text).toHaveValue("保留本次手改。");
    state.mode("normal");
    await comparison.getByRole("button", { name: "重新读取依据对照" }).click();
    await expect(
      comparison.getByRole("button", { name: "确认更新依据，正文保持原样" }),
    ).toBeEnabled();
    state.mode("write-hold");
    await comparison
      .getByRole("button", { name: "确认更新依据，正文保持原样" })
      .click();
    await expect.poll(() => state.counts.basis).toBe(2);
    await page.clock.fastForward(16000);
    expect(state.counts.basis).toBe(2);
    expect(state.counts.closed).toBe(1); // only the earlier discarded GET closed
    state.release();
    await expect(
      page.getByRole("alert").filter({ hasText: "请求结果尚未确认" }),
    ).toBeVisible();
    await expect(text).toHaveValue("保留本次手改。");
    expect(state.counts.unexpectedWrites).toEqual([]);
  } finally {
    await state.close();
  }
});
