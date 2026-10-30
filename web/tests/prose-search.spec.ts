import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";

async function fixture(
  page: Page,
  info: TestInfo,
  workspace = false,
  large = false,
) {
  const path = `/src/features/novel/${workspace ? "NovelWorkspace" : "NovelProseSearch"}.vue`;
  const compiled = await (await page.request.get(path)).text();
  const vue = compiled.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  const counts = {
    queries: [] as { source: string; query: string; after: string }[],
    writes: [] as string[],
    closed: 0,
  };
  let mode: "normal" | "hold" | "deny" | "fail" | "bad" | "limited" = "normal";
  let text = large ? `🌙${"林舟。".repeat(40)}` : "🌙林舟遇见林舟。",
    revision = 0;
  const held: (() => void)[] = [];
  const draft = (id = "a") => ({
    resource: {
      id: `control-${id}`,
      kind: "novel_chapter_control",
      name: "重逢",
      head: "head",
      revision: 1,
    },
    revision,
    base_head: "head",
    hash: revision ? "changed-a" : `source-${id}`,
    ready: true,
    reason: "",
    document: {
      schema: 1,
      chapter_id: `chapter-${id}`,
      body_id: `body-${id}`,
      detail: {
        kind: "resource",
        branch_id: "branch",
        id: "detail",
        revision: "detail-head",
        hash: "detail-hash",
      },
      settings_revision: 1,
      settings_hash: "settings",
      ip_head: "head",
      story_time: 7,
      perspective: "",
      title: "重逢",
      text,
      references: [],
    },
  });
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
        .end(`<!doctype html><meta charset="utf-8"><button id="leave">离开</button><pre id="target"></pre><div id="app"></div><script type="module">
import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};import Component from ${JSON.stringify(vite + path)};
const book={id:'book',project_id:'project',branch_id:'branch',settings_revision:1,stage:'WORLD_PLANNING',setup:{title:'雾城',genre:'悬疑',language:'简体中文',idea:'旧案',conflict:'旧约',target_words:300000,volume_count:3,chapter_min:2000,chapter_max:4000,characters:[]}};
window.expired=0;const app=createApp({render:()=>h(Component,{book,bookId:'book',project:{id:'project',name:'雾城',role:'owner',revision:1},actor:{id:'owner',workspace_id:'workspace',username:'owner',role:'owner'},onEdit:target=>document.querySelector('#target').textContent=JSON.stringify(target),onExpired:()=>window.expired++})});app.mount('#app');document.querySelector('#leave').onclick=()=>app.unmount();</script>`);
    let body = "";
    request.on("data", (chunk) => (body += chunk));
    request.on("end", () => {
      const input = body ? JSON.parse(body) : {};
      if (url.pathname.endsWith("/prose-search")) {
        expect(request.method).toBe("POST");
        expect(url.search).toBe("");
        counts.queries.push(input);
        const deliver = () => {
          const id = input.after ? "b" : "a";
          const chars = Array.from(id === "a" || large ? text : "码头无人。"),
            needle = Array.from(input.query);
          const matches = [];
          for (let i = 0; i <= chars.length - needle.length; ) {
            if (chars.slice(i, i + needle.length).join("") === input.query) {
              matches.push({
                start: i,
                end: i + needle.length,
                quote: input.query,
                before: chars.slice(Math.max(0, i - 32), i).join(""),
                after: chars
                  .slice(i + needle.length, i + needle.length + 32)
                  .join(""),
              });
              i += needle.length;
            } else i++;
          }
          return json({
            book_id: mode === "bad" ? "wrong" : "book",
            source: input.source,
            query: input.query,
            items: [
              {
                chapter_id: `chapter-${id}`,
                control_id: `control-${id}`,
                chapter_title: "重逢",
                origin:
                  mode === "limited"
                    ? "unavailable"
                    : input.source === "published" || revision === 0
                      ? "published"
                      : "private",
                source_hash: revision ? "changed-a" : `source-${id}`,
                revision,
                characters: chars.length,
                total_matches: mode === "limited" ? 0 : matches.length,
                truncated: false,
                reason: mode === "limited" ? "原正文来源不可读" : "",
                matches: mode === "limited" ? [] : matches,
              },
            ],
            scanned: 1,
            next_cursor: input.after ? "" : "control-a",
            notes: ["精确文字匹配，不代表语义影响覆盖"],
          });
        };
        if (mode === "hold") {
          held.push(deliver);
          response.on("close", () => counts.closed++);
          return;
        }
        if (mode === "deny") return json({ error: "当前权限已撤回" }, 403);
        if (mode === "fail") return json({ error: "暂时读取失败" }, 503);
        return deliver();
      }
      if (url.pathname.endsWith("/plans"))
        return json({ stage: "WORLD_PLANNING", resources: [], sources: [] });
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
      if (url.pathname.endsWith("/branches/branch"))
        return json({ id: "branch", head: "head", snapshot: { entities: [] } });
      if (url.pathname.endsWith("/draft")) {
        if (request.method === "PUT") {
          counts.writes.push(url.pathname);
          text = input.document.text;
          revision++;
        }
        return json(draft(url.pathname.includes("control-b") ? "b" : "a"));
      }
      if (url.pathname.endsWith("/release")) return json(null);
      if (request.method !== "GET") counts.writes.push(url.pathname);
      return json({ items: [] });
    });
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  await page.clock.install({ time: new Date("2026-10-29T13:00:00Z") });
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  return {
    counts,
    setMode: (value: typeof mode) => {
      mode = value;
    },
    release: () => held.splice(0).forEach((fn) => fn()),
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

test("小说工具提供正文查找，指定重复出现处准确定位，纯回查不创建私稿，修改返回重查", async ({
  page,
}, info) => {
  const f = await fixture(page, info, true);
  try {
    await page
      .getByRole("navigation", { name: "本书工作区" })
      .getByRole("button", { name: "审稿与导出", exact: true })
      .click();
    await page
      .getByRole("button", { name: "全书正文查找", exact: true })
      .click();
    await page.getByLabel("查找正文文字").fill("林舟");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "打开匹配 control-a 5" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "打开匹配 control-a 5" }).click();
    const field = page.getByLabel("章节正文", { exact: true });
    await expect(field).toHaveValue("🌙林舟遇见林舟。");
    await expect
      .poll(() =>
        field.evaluate((node: HTMLTextAreaElement) => node.selectionStart),
      )
      .toBe(6);
    await page
      .getByRole("button", { name: "保存并返回正文查找", exact: true })
      .click();
    expect(f.counts.writes).toHaveLength(0);
    await page.getByRole("button", { name: "打开匹配 control-a 5" }).click();
    await field.fill("🌙林舟遇见旧友。");
    await page
      .getByRole("button", { name: "保存并返回正文查找", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "全书正文查找", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "打开匹配 control-a 5" }),
    ).toHaveCount(0);
    await expect(page.getByRole("status")).toContainText("原匹配");
    await expect(page.getByLabel("查找正文文字")).toHaveValue("林舟");
    expect(f.counts.writes).toHaveLength(1);
  } finally {
    await f.close();
  }
});

test("多页姓名命中回查返回仍显示所选条目所在页", async ({ page }, info) => {
  const f = await fixture(page, info, true, true);
  try {
    await page
      .getByRole("navigation", { name: "本书工作区" })
      .getByRole("button", { name: "审稿与导出", exact: true })
      .click();
    await page
      .getByRole("button", { name: "全书正文查找", exact: true })
      .click();
    await page.getByLabel("查找正文文字").fill("林舟");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await page
      .getByRole("button", { name: "读取其余章节", exact: true })
      .click();
    await page
      .getByRole("navigation", { name: "正文匹配分页" })
      .getByRole("button", { name: "下一页", exact: true })
      .click();
    await page.getByRole("button", { name: "打开匹配 control-b 31" }).click();
    await page
      .getByRole("button", { name: "保存并返回正文查找", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "全书正文查找", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "打开匹配 control-b 31" }),
    ).toBeVisible();
    await expect(
      page.getByRole("navigation", { name: "正文匹配分页" }),
    ).toContainText("第 2 / 2 页");
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("查找保持精确查询与覆盖，临时故障保留结果，未应用输入不混版", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    await page.getByLabel("查找正文文字").fill("林舟");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await expect(page.getByText("已扫描 1 章", { exact: false })).toBeVisible();
    await page.getByLabel("查找正文文字").fill("码头");
    await expect(page.getByRole("status")).toContainText("未应用");
    f.setMode("fail");
    await page
      .getByRole("button", { name: "刷新当前结果", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText("暂时读取失败");
    await expect(
      page.getByRole("button", { name: "打开匹配 control-a 1" }),
    ).toBeVisible();
    f.setMode("normal");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await page
      .getByRole("button", { name: "读取其余章节", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "打开匹配 control-b 0" }),
    ).toBeVisible();
    expect(f.counts.queries.at(-1)?.query).toBe("码头");
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("换来源拒绝迟到结果，当前权限拒绝清空保护数据，停止和卸载取消只读查询", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    await page.getByLabel("查找正文文字").fill("林舟");
    f.setMode("hold");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await expect.poll(() => f.counts.queries.length).toBe(1);
    await page.getByLabel("正文来源").selectOption("published");
    f.release();
    await expect(
      page.getByRole("button", { name: "打开匹配 control-a 1" }),
    ).toHaveCount(0);
    f.setMode("normal");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "打开匹配 control-a 1" }),
    ).toBeVisible();
    f.setMode("hold");
    await page
      .getByRole("button", { name: "读取其余章节", exact: true })
      .click();
    await expect.poll(() => f.counts.queries.length).toBe(3);
    await page.getByRole("button", { name: "停止读取", exact: true }).click();
    f.release();
    f.setMode("deny");
    await page
      .getByRole("button", { name: "刷新当前结果", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText("撤回");
    await expect(
      page.getByRole("button", { name: "打开匹配 control-a 1" }),
    ).toHaveCount(0);
    f.setMode("hold");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await expect.poll(() => f.counts.queries.length).toBe(5);
    await page.locator("#leave").click();
    await expect.poll(() => f.counts.closed).toBeGreaterThan(0);
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("超时可重试，错误书籍拒绝，来源缺失不能当无命中", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    await page.getByLabel("查找正文文字").fill("林舟");
    f.setMode("hold");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await expect.poll(() => f.counts.queries.length).toBe(1);
    await page.clock.fastForward(16000);
    await expect(page.getByRole("alert")).toContainText("超时");
    f.setMode("bad");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("不匹配");
    f.setMode("limited");
    await page.getByRole("button", { name: "开始查找", exact: true }).click();
    await expect(
      page.getByText("原正文来源不可读", { exact: false }),
    ).toBeVisible();
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});
