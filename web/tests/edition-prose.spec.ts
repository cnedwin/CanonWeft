import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import { createHash } from "node:crypto";
import type { AddressInfo } from "node:net";

const oldText = "旧稿：雨打窗棂。\n\n林舟藏起了信。😀\n";
const newText = "新稿：风过屋檐。\n\n林舟交出了信。😀\n";
const sha = (value: string) => createHash("sha256").update(value).digest("hex");
const meta = (id = "chapter", hash = "old-document") => ({
  id,
  resource_id: "body",
  volume_id: "volume",
  volume: "卷一",
  title: "同名章",
  hash,
  head: "old-head",
  revision: 1,
  characters: Array.from(hash === "new-document" ? newText : oldText).length,
  included: true,
  warning: "",
});
async function fixture(page: Page, info: TestInfo, kind: "editor" | "compare") {
  const path = `/src/features/novel/${kind === "editor" ? "NovelChapterEditor" : "NovelEditionCompare"}.vue`;
  const compiled = await (await page.request.get(path)).text();
  const vue = compiled.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  const counts = {
    reads: [] as string[],
    writes: [] as { path: string; body: Record<string, unknown> }[],
    aborted: 0,
  };
  let mode: "normal" | "fail" | "deny" | "hold" | "mismatch" | "conflict" =
    "normal";
  let draft = {
    resource: {
      id: "control",
      name: "章节",
      kind: "novel_chapter_control",
      head: "",
      revision: 0,
    },
    revision: 4,
    hash: "current-document",
    base_head: "current-head",
    ready: true,
    reason: "",
    document: {
      schema: 1,
      chapter_id: "chapter",
      body_id: "body",
      title: "现在的章名",
      text: newText,
      detail: {
        kind: "resource",
        branch_id: "branch",
        id: "detail",
        revision: "detail-head",
        hash: "detail-hash",
      },
      settings_revision: 8,
      settings_hash: "current-settings",
      ip_head: "current-ip",
      story_time: 77,
      perspective: "character",
      writer_generation_id: "writer",
      references: [
        {
          source: { id: "research", hash: "ref" },
          quote: "当前引文",
          use: "参考",
          era: "",
          region: "",
          limitations: "",
        },
      ],
    },
  };
  const editions = [
    {
      id: "new",
      hash: sha("new"),
      name: "修订稿",
      title: "同一本书",
      published: true,
    },
    {
      id: "old",
      hash: sha("old"),
      name: "初稿",
      title: "同一本书",
      published: true,
    },
  ];
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
        .end(`<!doctype html><meta charset="utf-8"><button id="leave">离开</button><div id="app"></div><script type="module">
import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};
import Component from ${JSON.stringify(vite + path)};
const book={id:'book',branch_id:'branch',setup:{title:'小说',language:'简体中文'}};
const app=createApp({render:()=>h(Component,{book,bookId:'book',editions:${JSON.stringify(editions)},chapterId:'chapter',project:{id:'project',role:'owner'},actor:{id:'owner',role:'owner'}})});app.mount('#app');document.querySelector('#leave').onclick=()=>app.unmount();</script>`);
    if (request.method === "GET") {
      if (url.pathname.endsWith("/export/editions"))
        return json({ items: editions, next: "" });
      if (
        url.pathname.includes("/export/editions/") &&
        url.pathname.includes("/chapters/")
      ) {
        counts.reads.push(url.pathname);
        const edition = url.pathname.split("/editions/")[1]!.split("/")[0]!;
        const id = url.pathname.split("/chapters/")[1]!;
        const deliver = () =>
          json({
            book_id: "book",
            edition_id: edition,
            edition_hash: sha(edition),
            chapter: meta(
              mode === "mismatch" ? "wrong" : id,
              edition === "new" ? "new-document" : "old-document",
            ),
            text: edition === "new" ? newText : oldText,
            text_hash: sha(edition === "new" ? newText : oldText),
          });
        if (mode === "fail") return json({ error: "暂时不可读" }, 503);
        if (mode === "deny") return json({ error: "访问已经撤销" }, 403);
        if (mode === "hold") {
          held.push(deliver);
          response.on("close", () => counts.aborted++);
          return;
        }
        return deliver();
      }
      if (url.pathname.endsWith("/draft")) return json(draft);
      if (url.pathname.endsWith("/branches/branch"))
        return json({
          id: "branch",
          head: "current-ip",
          snapshot: {
            entities: [{ id: "character", kind: "character", name: "林舟" }],
          },
        });
      if (url.pathname.endsWith("/writing-card"))
        return json({ available: false, reason: "测试无需材料卡" }, 409);
      return json({ items: [] });
    }
    let raw = "";
    request.on("data", (chunk) => (raw += chunk));
    request.on("end", () => {
      const body = raw ? JSON.parse(raw) : {};
      if (url.pathname.endsWith("/export/preview")) {
        const chapters = [
          meta(
            "chapter",
            body.edition_id === "new" ? "new-document" : "old-document",
          ),
          meta("same-name", "same"),
          ...(body.edition_id === "old" ? [meta("retired")] : []),
        ];
        return json({
          hash: "preview",
          source: "edition",
          edition_id: body.edition_id,
          title: "同一本书",
          chapters,
          included: 2,
          missing: 0,
          warnings: 0,
          characters: 200,
        });
      }
      if (url.pathname.endsWith("/chapters") && request.method === "POST")
        return json(draft.resource);
      if (request.method === "PUT" && url.pathname.endsWith("/draft")) {
        counts.writes.push({ path: url.pathname, body });
        if (mode === "conflict")
          return json({ error: "另一页面已保存更新稿" }, 409);
        draft = {
          ...draft,
          revision: draft.revision + 1,
          hash: `saved-${draft.revision + 1}`,
          document: body.document,
        };
        return json(draft);
      }
      counts.writes.push({ path: url.pathname, body });
      return json({ error: "禁止测试中的额外写请求" }, 400);
    });
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  await page.clock.install({ time: new Date("2026-10-29T07:00:00Z") });
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  return {
    counts,
    draft: () => structuredClone(draft),
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

test("当前章与旧成书对照，明确取回只换正文并可撤销一次", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "editor");
  try {
    await page
      .getByRole("button", { name: "对照与取回旧稿", exact: true })
      .click();
    const desk = page.getByRole("region", { name: "本章旧稿对照" });
    await desk.getByLabel("旧成书稿本").selectOption("old");
    await desk.getByRole("button", { name: "读取本章旧正文" }).click();
    await expect(desk.getByLabel("正文差异对照")).toContainText(
      "旧稿：雨打窗棂",
    );
    const original = state.draft();
    expect(state.counts.writes).toHaveLength(0);
    await desk
      .getByLabel("我确认用旧正文替换当前私人稿正文，并沿现有流程自动暂存")
      .check();
    await desk
      .getByRole("button", { name: "取回这版正文", exact: true })
      .click();
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      oldText,
    );
    await page.clock.runFor(700);
    await expect.poll(() => state.counts.writes.length).toBe(1);
    const body = state.counts.writes[0]!.body.document as Record<
      string,
      unknown
    >;
    expect({ ...body, text: original.document.text }).toEqual(
      original.document,
    );
    await expect(
      desk.getByRole("button", { name: "撤销这次旧稿取回" }),
    ).toBeEnabled();
    await desk.getByRole("button", { name: "撤销这次旧稿取回" }).click();
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      newText,
    );
    await page.clock.runFor(700);
    await expect.poll(() => state.counts.writes.length).toBe(2);
  } finally {
    await state.close();
  }
});

test("当前手改与版本变化清除取回确认，旧读取迟到不覆盖新稿或选择", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "editor");
  try {
    await page
      .getByRole("button", { name: "对照与取回旧稿", exact: true })
      .click();
    const desk = page.getByRole("region", { name: "本章旧稿对照" });
    await desk.getByLabel("旧成书稿本").selectOption("old");
    await desk.getByRole("button", { name: "读取本章旧正文" }).click();
    await desk
      .getByLabel("我确认用旧正文替换当前私人稿正文，并沿现有流程自动暂存")
      .check();
    await page
      .getByLabel("章节正文", { exact: true })
      .fill("作者刚补的关键高光");
    await expect(
      desk.getByLabel("我确认用旧正文替换当前私人稿正文，并沿现有流程自动暂存"),
    ).not.toBeChecked();
    await page.clock.runFor(700);
    await expect.poll(() => state.counts.writes.length).toBe(1);
    state.mode("hold");
    await desk.getByRole("button", { name: "读取本章旧正文" }).click();
    await desk.getByLabel("旧成书稿本").selectOption("new");
    state.mode("normal");
    state.release();
    await expect(
      desk.getByRole("button", { name: "取回这版正文", exact: true }),
    ).toBeDisabled();
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      "作者刚补的关键高光",
    );
    expect(state.counts.writes).toHaveLength(1);
  } finally {
    await state.close();
  }
});

test("两版成书按稳定章节ID读取正文，同名不混；差异读取没有写入", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "compare");
  try {
    await page.getByRole("button", { name: "比较这两个稿本" }).click();
    await page
      .getByRole("button", { name: "对照本章正文 chapter", exact: true })
      .click();
    await expect(page.getByLabel("正文差异对照")).toContainText(
      "旧稿：雨打窗棂",
    );
    await expect(page.getByLabel("正文差异对照")).toContainText(
      "新稿：风过屋檐",
    );
    expect(state.counts.reads).toEqual(
      expect.arrayContaining([
        "/api/novels/book/export/editions/old/chapters/chapter",
        "/api/novels/book/export/editions/new/chapters/chapter",
      ]),
    );
    expect(state.counts.writes).toHaveLength(0);
    const priorReads = state.counts.reads.length;
    await page
      .getByRole("button", { name: "对照本章正文 retired", exact: true })
      .click();
    await expect(page.getByLabel("正文差异对照")).toContainText(
      "旧稿：雨打窗棂",
    );
    await expect(
      page.getByRole("status").filter({ hasText: "此章不存在或未纳入正文" }),
    ).toBeVisible();
    expect(state.counts.reads.slice(priorReads)).toEqual([
      "/api/novels/book/export/editions/old/chapters/retired",
    ]);
    await page.getByLabel("比较原稿本").selectOption("new");
    await expect(page.getByLabel("正文差异对照")).toHaveCount(0);
  } finally {
    await state.close();
  }
});

test("旧稿读取有界恢复、错章拒绝及当前权限撤销清除受保护正文", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "editor");
  try {
    await page
      .getByRole("button", { name: "对照与取回旧稿", exact: true })
      .click();
    const desk = page.getByRole("region", { name: "本章旧稿对照" });
    await desk.getByLabel("旧成书稿本").selectOption("old");
    state.mode("hold");
    await desk.getByRole("button", { name: "读取本章旧正文" }).click();
    await page.clock.runFor(16000);
    await expect(desk.getByRole("alert")).toContainText("超时");
    state.mode("mismatch");
    await desk.getByRole("button", { name: "读取本章旧正文" }).click();
    await expect(desk.getByRole("alert")).toContainText("不一致");
    state.mode("normal");
    await desk.getByRole("button", { name: "读取本章旧正文" }).click();
    await expect(desk.getByLabel("正文差异对照")).toContainText(
      "旧稿：雨打窗棂",
    );
    state.mode("deny");
    await desk.getByRole("button", { name: "读取本章旧正文" }).click();
    await expect(desk.getByLabel("正文差异对照")).toHaveCount(0);
    await expect(desk.getByLabel("旧成书稿本").locator("option")).toHaveCount(
      1,
    );
    expect(state.counts.writes).toHaveLength(0);
  } finally {
    await state.close();
  }
});

test("取回遇保存冲突保留本页文本，不覆盖另一页稿件、不发送模型请求", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "editor");
  try {
    await page
      .getByRole("button", { name: "对照与取回旧稿", exact: true })
      .click();
    const desk = page.getByRole("region", { name: "本章旧稿对照" });
    await desk.getByLabel("旧成书稿本").selectOption("old");
    await desk.getByRole("button", { name: "读取本章旧正文" }).click();
    await desk
      .getByLabel("我确认用旧正文替换当前私人稿正文，并沿现有流程自动暂存")
      .check();
    state.mode("conflict");
    await desk
      .getByRole("button", { name: "取回这版正文", exact: true })
      .click();
    await page.clock.runFor(700);
    await expect(
      page.getByRole("alert").filter({ hasText: "另一页面" }),
    ).toBeVisible();
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      oldText,
    );
    expect(state.draft().document.text).toBe(newText);
    await page.clock.runFor(16000);
    expect(state.counts.writes).toHaveLength(1);
  } finally {
    await state.close();
  }
});
