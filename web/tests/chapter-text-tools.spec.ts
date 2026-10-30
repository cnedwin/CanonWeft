import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";
import { createHash } from "node:crypto";
import {
  chapterTextareaSelection,
  chapterSourceSelection,
  findChapterMatches,
  previewChapterReplacement,
} from "../src/features/novel/chapterTextTools";
import {
  classifyChapterSave,
  sameChapterDocument,
} from "../src/features/novel/chapterSaveRecovery";
import type { ChapterDraft } from "../src/features/novel/chapterTypes";

const original = "🌙林舟问：林舟？\n林舟与临舟。$& [门]";
async function fixture(
  page: Page,
  info: TestInfo,
  initial = original,
  navigation = false,
  reviewOrigin = false,
  referenceMode = false,
) {
  const bookId = referenceMode ? "10".repeat(16) : "book";
  const chapterId = referenceMode ? "20".repeat(16) : "chapter";
  const controlId = referenceMode ? "30".repeat(16) : "control";
  const previousId = "11".repeat(16),
    nextId = "33".repeat(16);
  let referenceText = "🌧林舟\r\n旧章伏笔，尚未揭晓。";
  let referenceRevision = 1,
    referenceStatus = 200,
    holdReference = false;
  let holdDirectory = false;
  const hash = (text: string) =>
    createHash("sha256").update(text).digest("hex");
  const vite = new URL(info.project.use.baseURL!).origin;
  const module = "/src/features/novel/NovelChapterEditor.vue";
  const compiled = await (await page.request.get(module)).text();
  const vue = compiled.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const document = {
    schema: 1,
    chapter_id: chapterId,
    body_id: "body",
    title: "第一章",
    text: initial,
    references: [],
    story_time: 12,
    perspective: "person",
    detail: {
      kind: "resource",
      id: "detail",
      branch_id: "branch",
      revision: "head",
      hash: "basis",
    },
    settings_revision: 3,
    settings_hash: "settings",
    ip_head: "ip-head",
  };
  let draft = {
    resource: {
      id: controlId,
      branch_id: "branch",
      kind: "novel_chapter_control",
      name: `章节工作稿 · ${chapterId}`,
      head: "",
      revision: 1,
    },
    revision: 1,
    base_head: "",
    hash: "saved-1",
    ready: true,
    reason: "",
    document,
  };
  const writes: (typeof document)[] = [];
  const calls: { path: string; method: string }[] = [];
  let chapters: unknown = {
    items: [
      { id: "z-previous", title: "序章", ready: true, reason: "" },
      { id: "chapter", title: "第一章", ready: true, reason: "" },
      { id: "a-next", title: "第二章 夜雨", ready: true, reason: "" },
      { id: "unready", title: "第三章", ready: false, reason: "细纲尚未确认" },
    ],
  };
  let chaptersStatus = 200;
  let holdSave = false;
  let lostSave: "before" | "after" | null = null;
  const historyRoot = `/api/novels/${bookId}/chapters/${controlId}/history`;
  const history: Record<string, unknown>[] = [];
  const historyReasons: string[] = [];
  let historyStatus = 200,
    holdHistory = false,
    loseCheckpoint = false;
  let holdCheckpoint = false,
    holdOperation = false,
    checkpointReplies = 0,
    operationHolds = 0;
  const waiting: (() => void)[] = [];
  const server = createServer(async (request, response) => {
    const url = new URL(request.url!, "http://127.0.0.1");
    const path = url.pathname;
    calls.push({ path, method: request.method! });
    const json = (value: unknown, status = 200) => {
      response.writeHead(status, { "Content-Type": "application/json" });
      response.end(JSON.stringify(value));
    };
    if (path === historyRoot || path.startsWith(historyRoot + "/")) {
      if (historyStatus !== 200)
        return json({ error: "章节修订权限已撤销" }, historyStatus);
      if (request.method === "POST") {
        let raw = "";
        for await (const part of request) raw += part;
        const input = JSON.parse(raw);
        if (input.revision !== draft.revision || input.hash !== draft.hash)
          return json({ error: "章稿已变化" }, 409);
        const item = {
          id: hash(input.operation_id).slice(0, 32),
          chapter_id: chapterId,
          source_revision: draft.revision,
          source_hash: draft.hash,
          source_base_head: draft.base_head,
          text_hash: hash(draft.document.text),
          title: draft.document.title,
          characters: Array.from(draft.document.text).length,
          kind: "manual",
          note: input.note,
          created_at: new Date().toISOString(),
          operation_id: input.operation_id,
          text: draft.document.text,
        };
        history.unshift(item);
        if (holdCheckpoint) {
          holdCheckpoint = false;
          await new Promise<void>((resolve) => waiting.push(resolve));
        }
        checkpointReplies++;
        if (loseCheckpoint) {
          loseCheckpoint = false;
          response.writeHead(201, { "Content-Type": "application/json" });
          return response.end("{");
        }
        return json(item, 201);
      }
      const id = path.slice(historyRoot.length + 1);
      const item = history.find((value) => value.id === id);
      const result = id
        ? item
        : {
            items: history
              .filter(
                (v) =>
                  !url.searchParams.has("operation_id") ||
                  v.operation_id === url.searchParams.get("operation_id"),
              )
              .map((item) => {
                const meta = { ...item };
                delete meta.text;
                return meta;
              }),
            next: "",
          };
      if (holdHistory && id) {
        holdHistory = false;
        await new Promise<void>((resolve) => waiting.push(resolve));
      }
      if (holdOperation && url.searchParams.has("operation_id")) {
        holdOperation = false;
        operationHolds++;
        await new Promise<void>((resolve) => waiting.push(resolve));
      }
      return json(result ?? { error: "版本不存在" }, result ? 200 : 404);
    }
    if (path === `/api/novels/${bookId}/chapter-references`) {
      if (holdDirectory) {
        holdDirectory = false;
        await new Promise<void>((resolve) => waiting.push(resolve));
      }
      return json({
        book_id: bookId,
        complete: true,
        notes: ["仅为目录参考，不代表生成资格。"],
        items: [previousId, chapterId, nextId].map((id, index) => ({
          chapter_id: id,
          control_id: index === 1 ? controlId : id,
          title: ["旧城来信", "第一章", "后续安排"][index],
          volume_id: "44".repeat(16),
          volume: "旧城卷",
          reason: "",
        })),
      });
    }
    if (path.startsWith(`/api/novels/${bookId}/chapter-references/`)) {
      if (referenceStatus !== 200)
        return json(
          {
            error:
              referenceStatus === 403
                ? "参考原文权限已撤销"
                : "参考原文暂不可读",
          },
          referenceStatus,
        );
      const id = path.split("/").at(-1)!;
      const source = url.searchParams.get("source");
      const text =
        source === "published"
          ? "已采用稿：原来的旧城线索。"
          : id === nextId
            ? "后章原文，只有作者知道的安排。"
            : referenceText;
      const value = {
        book_id: bookId,
        chapter_id: id,
        control_id: id,
        source,
        origin: source === "published" ? "published" : "private",
        title: id === previousId ? "旧城来信" : "后续安排",
        revision: referenceRevision,
        source_hash: hash("document:" + text),
        text_hash: hash(text),
        characters: Array.from(text).length,
        text,
        reason: "当前写作依据仍需复核。",
      };
      if (holdReference) {
        holdReference = false;
        await new Promise<void>((resolve) => waiting.push(resolve));
      }
      return json(value);
    }
    if (path === `/api/novels/${bookId}/writing-batches/chapters`)
      return json(chapters, chaptersStatus);
    if (path === `/api/novels/${bookId}/chapters` && request.method === "POST")
      return json(draft.resource);
    if (path === `/api/novels/${bookId}/chapters/${controlId}/draft`) {
      if (request.method === "PUT") {
        let body = "";
        for await (const chunk of request) body += chunk;
        const value = JSON.parse(body);
        historyReasons.push(value.history_reason ?? "");
        writes.push(structuredClone(value.document));
        if (lostSave === "before") {
          lostSave = null;
          response.writeHead(200, { "Content-Type": "application/json" });
          return response.end("{");
        }
        if (holdSave)
          await new Promise<void>((resolve) => waiting.push(resolve));
        draft = {
          ...draft,
          document: value.document,
          revision: draft.revision + 1,
          hash: `saved-${draft.revision + 1}`,
        };
        if (lostSave === "after") {
          lostSave = null;
          response.writeHead(200, { "Content-Type": "application/json" });
          return response.end("{");
        }
      }
      return json(draft);
    }
    if (path === "/api/projects/project/branches/branch")
      return json({
        id: "branch",
        head: "head",
        name: "正史",
        snapshot: {
          entities: [
            { id: "person", name: "林舟", kind: "character", status: "active" },
          ],
          assertions: [],
          rules: [],
        },
      });
    if (path.endsWith("/writing-card"))
      return json({
        revision: draft.revision,
        hash: draft.hash,
        card: {
          title: "夜访",
          goal: "寻找旧案线索",
          scene_goals: [],
          guardrails: [],
          recent_plan: [],
          sources: [],
          notes: [],
        },
      });
    if (path.endsWith("/research"))
      return json({ items: [], references: [], issues: [] });
    return json({ error: `未约定接口 ${request.method} ${path}` }, 404);
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  await page.route("**/chapter-edit-fixture", (route) =>
    route.fulfill({
      contentType: "text/html",
      body: `<!doctype html><html><body><div id="app"></div><script type="module">
  import {createApp,h} from ${JSON.stringify(vue)};
  import Editor from ${JSON.stringify(vite + module)};
  import ${JSON.stringify(vite + "/src/style.css")};
  window.__chapterNavigations=[];
  const app=createApp({render:()=>h(Editor,{book:{id:${JSON.stringify(bookId)},project_id:'project',branch_id:'branch',setup:{title:'雾城',genre:'悬疑',chapter_min:2000,chapter_max:4000}},project:{id:'project',role:'owner'},actor:{id:'author',workspace_id:'workspace',role:'owner'},chapterId:${JSON.stringify(chapterId)},allowChapterNavigation:${navigation},reviewTarget:${reviewOrigin ? "{chapter_id:'chapter',control_id:'control',quote:'林舟'}" : "null"},onNavigate:(id)=>window.__chapterNavigations.push(id)})});app.mount('#app');window.__chapterEditApp=app;
  </script></body></html>`,
    }),
  );
  // The real browser uses same-origin requests to the loopback fixture via Vite.
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const response = await page.request.fetch(
      origin + new URL(request.url()).pathname + new URL(request.url()).search,
      {
        method: request.method(),
        data: request.postDataBuffer() ?? undefined,
        headers: request.headers(),
      },
    );
    await route.fulfill({ response });
  });
  await page.goto(vite + "/chapter-edit-fixture");
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    initial.replace(/\r\n?/g, "\n"),
  );
  return {
    writes,
    calls,
    history,
    historyReasons,
    historyStatus: (status: number) => {
      historyStatus = status;
    },
    holdHistory: () => {
      holdHistory = true;
    },
    loseCheckpoint: () => {
      loseCheckpoint = true;
    },
    holdCheckpoint: () => {
      holdCheckpoint = true;
    },
    holdOperation: () => {
      holdOperation = true;
    },
    checkpointReplies: () => checkpointReplies,
    operationHolds: () => operationHolds,
    original: structuredClone(document),
    previousId,
    nextId,
    referenceText: () => referenceText,
    changeReference: (text: string) => {
      referenceText = text;
      referenceRevision++;
    },
    denyReference: (status: number) => {
      referenceStatus = status;
    },
    holdReference: () => {
      holdReference = true;
    },
    holdDirectory: () => {
      holdDirectory = true;
    },
    setChapters: (value: unknown, status = 200) => {
      chapters = value;
      chaptersStatus = status;
    },
    loseSave: (commit = true) => {
      lostSave = commit ? "after" : "before";
    },
    hold: () => {
      holdSave = true;
    },
    release: () => {
      holdSave = false;
      waiting.splice(0).forEach((resolve) => resolve());
    },
    close: async () => {
      waiting.splice(0).forEach((resolve) => resolve());
      await page.evaluate(() =>
        (
          window as unknown as { __chapterEditApp: { unmount: () => void } }
        ).__chapterEditApp.unmount(),
      );
      await page.unrouteAll({ behavior: "wait" });
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

async function tools(page: Page) {
  await page
    .getByRole("button", { name: "章内查找与替换", exact: true })
    .click();
  return page.getByRole("region", { name: "章内查找与替换", exact: true });
}

test("章节历史命名留版、原文对照取回与撤销保留当前章信息", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    await page
      .getByRole("button", { name: "章节修订历史", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "章节修订历史",
      exact: true,
    });
    await panel
      .getByLabel("版本备注", { exact: true })
      .fill("第一版，保留原始换行");
    await panel
      .getByRole("button", { name: "保存当前版本", exact: true })
      .click();
    await expect(
      panel.getByLabel("历史版本", { exact: true }).locator("option").last(),
    ).toContainText("第一版，保留原始换行");
    expect(state.history).toHaveLength(1);
    const manuscript = page.getByLabel("章节正文", { exact: true });
    await manuscript.fill("作者继续修改后的新正文。");
    await expect.poll(() => state.writes.length).toBe(1);
    await panel
      .getByRole("button", { name: "读取所选版本", exact: true })
      .click();
    await expect(
      panel.getByRole("region", { name: "正文差异对照" }),
    ).toBeVisible();
    await page.screenshot({
      path: info.outputPath("history-desktop.png"),
      fullPage: true,
    });
    await panel.getByRole("checkbox", { name: /我确认用历史正文/ }).check();
    await panel
      .getByRole("button", { name: "取回这版正文", exact: true })
      .click();
    await expect.poll(() => state.writes.length).toBe(2);
    expect(state.writes[1]).toEqual(state.original);
    expect(state.historyReasons[1]).toBe("before_restore");
    await panel
      .getByRole("button", { name: "撤销这次旧稿取回", exact: true })
      .click();
    await expect(manuscript).toHaveValue("作者继续修改后的新正文。");
  } finally {
    await state.close();
  }
});

test("历史留版回执丢失只读核对，不重发或阻断本页手稿", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    await page
      .getByRole("button", { name: "章节修订历史", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "章节修订历史",
      exact: true,
    });
    state.loseCheckpoint();
    await panel.getByLabel("版本备注", { exact: true }).fill("回执未知");
    await panel
      .getByRole("button", { name: "保存当前版本", exact: true })
      .click();
    await expect(
      panel.getByRole("button", { name: "核对留版结果", exact: true }),
    ).toBeVisible();
    await expect(
      panel.getByRole("button", { name: "关闭修订历史", exact: true }),
    ).toBeDisabled();
    await panel
      .getByRole("button", { name: "核对留版结果", exact: true })
      .click();
    await expect(panel.getByText("版本已留存", { exact: false })).toBeVisible();
    expect(state.history).toHaveLength(1);
    expect(
      state.calls.filter(
        (v) => v.path.endsWith("/history") && v.method === "POST",
      ),
    ).toHaveLength(1);
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      original,
    );
  } finally {
    await state.close();
  }
});

test("章节历史取回确认随手改失效，权限恢复保留手稿和备注", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    await page
      .getByRole("button", { name: "章节修订历史", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "章节修订历史",
      exact: true,
    });
    await panel
      .getByRole("button", { name: "保存当前版本", exact: true })
      .click();
    await expect.poll(() => state.history.length).toBe(1);
    const manuscript = page.getByLabel("章节正文", { exact: true });
    await manuscript.fill("作者新正文。");
    await expect.poll(() => state.writes.length).toBe(1);
    await panel
      .getByRole("button", { name: "读取所选版本", exact: true })
      .click();
    await panel.getByRole("checkbox", { name: /我确认用历史正文/ }).check();
    await manuscript.fill("作者又修改正文。");
    await expect(
      panel.getByRole("button", { name: "取回这版正文", exact: true }),
    ).toBeDisabled();
    await expect(
      panel.getByRole("checkbox", { name: /我确认用历史正文/ }),
    ).not.toBeChecked();
    await panel
      .getByLabel("版本备注", { exact: true })
      .fill("尚未提交的版本备注");
    state.historyStatus(403);
    await panel
      .getByRole("button", { name: "刷新修订历史", exact: true })
      .click();
    await expect(panel.getByRole("alert")).toContainText("权限");
    await expect(
      panel.getByRole("region", { name: "正文差异对照" }),
    ).toHaveCount(0);
    await expect(manuscript).toHaveValue("作者又修改正文。");
    await expect(panel.getByLabel("版本备注", { exact: true })).toHaveValue(
      "尚未提交的版本备注",
    );
    state.historyStatus(200);
    await panel
      .getByRole("button", { name: "刷新修订历史", exact: true })
      .click();
    await expect(
      panel.getByLabel("历史版本", { exact: true }).locator("option"),
    ).toHaveCount(2);
    await panel
      .getByRole("button", { name: "读取所选版本", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath("history-mobile.png"),
      fullPage: true,
    });
  } finally {
    await state.close();
  }
});

test("关闭历史时的迟到原文不污染重新打开的当前视图", async ({ page }, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    await page
      .getByRole("button", { name: "章节修订历史", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "章节修订历史",
      exact: true,
    });
    await panel
      .getByRole("button", { name: "保存当前版本", exact: true })
      .click();
    await expect.poll(() => state.history.length).toBe(1);
    state.holdHistory();
    await panel
      .getByRole("button", { name: "读取所选版本", exact: true })
      .click();
    await expect(
      panel.getByRole("button", { name: "正在读取…", exact: true }),
    ).toBeVisible();
    await panel
      .getByRole("button", { name: "关闭修订历史", exact: true })
      .click();
    await page
      .getByRole("button", { name: "章节修订历史", exact: true })
      .click();
    await panel
      .getByLabel("版本备注", { exact: true })
      .fill("新视图保留的备注");
    state.release();
    await expect(
      panel.getByRole("region", { name: "正文差异对照" }),
    ).toHaveCount(0);
    await expect(panel.getByLabel("版本备注", { exact: true })).toHaveValue(
      "新视图保留的备注",
    );
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      original,
    );
    expect(state.writes).toHaveLength(0);
  } finally {
    await state.close();
  }
});

test("跨章参照保持本章编辑，精确CRLF选段经重核后带入原精修输入", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    expect(
      state.calls.some((call) => call.path.includes("chapter-references")),
    ).toBe(false);
    await page
      .getByRole("button", { name: "跨章原文参照", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "跨章原文参照",
      exact: true,
    });
    await panel
      .getByRole("button", { name: "参照上一章", exact: true })
      .click();
    const source = panel.getByLabel("参考章节原文", { exact: true });
    await expect(source).toHaveValue(
      state.referenceText().replace(/\r\n/g, "\n"),
    );
    await expect(panel).toContainText("当前作者已保存私人稿");
    await page
      .getByLabel("章节正文", { exact: true })
      .fill(original + "\n作者继续补写。");
    const notes = page.getByLabel("这次希望怎样修改", { exact: true });
    await notes.fill("原有精修要求");
    await source.evaluate((field: HTMLTextAreaElement) => {
      field.setSelectionRange(2, 9);
      field.dispatchEvent(new Event("select", { bubbles: true }));
    });
    await panel
      .getByRole("button", { name: "将选段加入精修要求", exact: true })
      .click();
    await expect(notes).toHaveValue(
      /原有精修要求[\s\S]*作者参考[\s\S]*林舟\n旧章伏笔/,
    );
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      original + "\n作者继续补写。",
    );
    await expect.poll(() => state.writes.length).toBe(1);
    expect(state.writes[0].text).toBe(original + "\n作者继续补写。");
    expect(state.calls.filter((call) => call.method === "POST")).toHaveLength(
      1,
    );
    expect(
      state.calls.some((call) =>
        /generate|writer|prepare|start/.test(call.path),
      ),
    ).toBe(false);
    await page.screenshot({
      path: info.outputPath("reference-desktop.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath("reference-mobile.png"),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "进入专注写作", exact: true })
      .click();
    await expect(panel).toBeHidden();
    await page
      .getByRole("button", { name: "退出专注写作", exact: true })
      .click();
    await expect(source).toHaveValue(
      state.referenceText().replace(/\r\n/g, "\n"),
    );
  } finally {
    await state.close();
  }
});

test("跨章参照区分正式来源，旧选段在来源变化或重核失败时不能带入", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    await page
      .getByRole("button", { name: "跨章原文参照", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "跨章原文参照",
      exact: true,
    });
    await panel
      .getByRole("button", { name: "参照上一章", exact: true })
      .click();
    const source = panel.getByLabel("参考章节原文", { exact: true });
    await expect(source).toBeVisible();
    await source.evaluate((field: HTMLTextAreaElement) => {
      field.setSelectionRange(2, 4);
      field.dispatchEvent(new Event("select", { bubbles: true }));
    });
    state.changeReference("这章已经有另一版新原文。");
    await panel
      .getByRole("button", { name: "将选段加入精修要求", exact: true })
      .click();
    await expect(panel).toContainText("参考章已变化");
    await expect(
      page.getByLabel("这次希望怎样修改", { exact: true }),
    ).toHaveValue("");
    await panel.getByText("选择参考章节与来源", { exact: true }).click();
    await panel
      .getByLabel("参考正文来源", { exact: true })
      .selectOption("published");
    await panel
      .getByRole("button", { name: "读取参考原文", exact: true })
      .click();
    await expect(source).toHaveValue("已采用稿：原来的旧城线索。");
    await expect(panel).toContainText("已采用稿");
    await source.evaluate((field: HTMLTextAreaElement) => {
      field.setSelectionRange(0, 3);
      field.dispatchEvent(new Event("select", { bubbles: true }));
    });
    state.denyReference(403);
    await panel
      .getByRole("button", { name: "将选段加入精修要求", exact: true })
      .click();
    await expect(source).toHaveCount(0);
    await expect(panel).toContainText("参考原文权限已撤销");
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      original,
    );
    expect(state.writes).toHaveLength(0);
  } finally {
    await state.close();
  }
});

test("跨章参照迟到结果不串章，过长引用及精修总容量不会覆盖输入", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    await page
      .getByRole("button", { name: "跨章原文参照", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "跨章原文参照",
      exact: true,
    });
    state.holdReference();
    await panel
      .getByRole("button", { name: "参照上一章", exact: true })
      .click();
    await expect
      .poll(
        () =>
          state.calls.filter((call) => call.path.endsWith(state.previousId))
            .length,
      )
      .toBe(1);
    await panel
      .getByLabel("参考章节", { exact: true })
      .selectOption(state.nextId);
    await panel
      .getByRole("button", { name: "读取参考原文", exact: true })
      .click();
    const source = panel.getByLabel("参考章节原文", { exact: true });
    await expect(source).toHaveValue("后章原文，只有作者知道的安排。");
    state.release();
    await expect(source).toHaveValue("后章原文，只有作者知道的安排。");
    state.changeReference("长".repeat(1501));
    await panel
      .getByRole("button", { name: "参照上一章", exact: true })
      .click();
    await expect(source).toHaveValue("长".repeat(1501));
    await source.evaluate((field: HTMLTextAreaElement) => {
      field.select();
      field.dispatchEvent(new Event("select", { bubbles: true }));
    });
    await expect(panel).toContainText("最多1500字");
    await expect(
      panel.getByRole("button", { name: "将选段加入精修要求", exact: true }),
    ).toBeDisabled();
    const notes = page.getByLabel("这次希望怎样修改", { exact: true });
    await notes.fill("原".repeat(3950));
    await source.evaluate((field: HTMLTextAreaElement) => {
      field.setSelectionRange(0, 1);
      field.dispatchEvent(new Event("select", { bubbles: true }));
    });
    await panel
      .getByRole("button", { name: "将选段加入精修要求", exact: true })
      .click();
    await expect(notes).toHaveValue("原".repeat(3950));
    await expect(
      page.getByText("精修要求容量不足", { exact: false }),
    ).toBeVisible();
    expect(state.writes).toHaveLength(0);
  } finally {
    await state.close();
  }
});

test("跨章参照慢目录返回不丢失手动回查的已移出章节", async ({ page }, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    state.holdDirectory();
    await page
      .getByRole("button", { name: "跨章原文参照", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "跨章原文参照",
      exact: true,
    });
    await panel.getByText("按准确章节编号回查", { exact: true }).click();
    await panel
      .getByLabel("准确章节编号", { exact: true })
      .fill("77".repeat(16));
    await panel
      .getByRole("button", { name: "读取参考原文", exact: true })
      .click();
    const source = panel.getByLabel("参考章节原文", { exact: true });
    await expect(source).toHaveValue(
      state.referenceText().replace(/\r\n/g, "\n"),
    );
    state.release();
    await expect(
      panel.getByText("正在读取已有规划目录…", { exact: true }),
    ).toHaveCount(0);
    await expect(source).toHaveValue(
      state.referenceText().replace(/\r\n/g, "\n"),
    );
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      original,
    );
    expect(state.writes).toHaveLength(0);
  } finally {
    await state.close();
  }
});

test("章节跳转遵从规划顺序，筛选标题且不自动保存或生成", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, true);
  try {
    const panel = page.getByRole("region", { name: "章节跳转", exact: true });
    await expect(panel).toContainText("第 2 / 4 章");
    await panel.getByRole("button", { name: "上一章", exact: true }).click();
    await panel.getByRole("button", { name: "下一章", exact: true }).click();
    await panel.getByLabel("查找要打开的章节", { exact: true }).fill("夜雨");
    await panel.getByLabel("目标章节", { exact: true }).selectOption("a-next");
    await panel
      .getByRole("button", { name: "打开所选章", exact: true })
      .click();
    expect(
      await page.evaluate(
        () =>
          (window as unknown as { __chapterNavigations: string[] })
            .__chapterNavigations,
      ),
    ).toEqual(["z-previous", "a-next", "a-next"]);
    await panel.getByLabel("查找要打开的章节", { exact: true }).fill("第三章");
    await expect(panel.getByRole("option", { name: /第三章/ })).toHaveAttribute(
      "disabled",
      "",
    );
    await expect(
      panel.getByRole("button", { name: "打开所选章", exact: true }),
    ).toBeDisabled();
    state.setChapters({
      items: [
        { id: "chapter", title: "第一章", ready: true, reason: "" },
        ...Array.from({ length: 501 }, (_, index) => ({
          id: `chapter-${index}`,
          title: `长篇${index}`,
          ready: true,
          reason: "",
        })),
      ],
    });
    await panel
      .getByRole("button", { name: "刷新章节顺序", exact: true })
      .click();
    await panel.getByLabel("查找要打开的章节", { exact: true }).fill("");
    await expect(panel).toContainText("找到 502 章，显示前 200 章");
    await expect(panel.getByRole("option")).toHaveCount(201);
    await panel.getByLabel("查找要打开的章节", { exact: true }).fill("长篇500");
    await panel
      .getByLabel("目标章节", { exact: true })
      .selectOption("chapter-500");
    await expect(
      panel.getByRole("button", { name: "打开所选章", exact: true }),
    ).toBeEnabled();
    expect(state.writes).toHaveLength(0);
    expect(state.calls.filter((call) => call.method === "POST")).toEqual([
      { path: "/api/novels/book/chapters", method: "POST" },
    ]);
  } finally {
    await state.close();
  }
});

test("章节跳转保留保存中和结果未知的手稿，也保护未处理精修备注", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, true);
  try {
    const panel = page.getByRole("region", { name: "章节跳转", exact: true });
    const next = panel.getByRole("button", { name: "下一章", exact: true });
    await expect(next).toBeEnabled();
    const notes = page.getByLabel("这次希望怎样修改", { exact: true });
    await notes.fill("保留结尾伏笔");
    await expect(next).toBeDisabled();
    await notes.fill("");
    await expect(next).toBeEnabled();
    const editorialNotes = page.getByLabel("题材与编辑重点", { exact: true });
    await editorialNotes.fill("本章的关键线索不能丢失");
    await expect(next).toBeDisabled();
    await expect(editorialNotes).toHaveValue("本章的关键线索不能丢失");
    await editorialNotes.fill("");
    await expect(next).toBeEnabled();
    state.hold();
    await page
      .getByLabel("章节正文", { exact: true })
      .fill(original + "\n还没保存的补写");
    await expect(next).toBeDisabled();
    await expect.poll(() => state.writes.length).toBe(1);
    await expect(next).toBeDisabled();
    state.loseSave();
    state.release();
    await expect(
      page.getByText("上次保存待核对，本页稿仍保留", { exact: false }),
    ).toBeVisible();
    await expect(next).toBeDisabled();
    expect(
      await page.evaluate(
        () =>
          (window as unknown as { __chapterNavigations: string[] })
            .__chapterNavigations,
      ),
    ).toEqual([]);
    await page
      .getByRole("button", { name: "核对上次保存", exact: true })
      .click();
    await expect(next).toBeEnabled();
    expect(state.writes).toHaveLength(1);
  } finally {
    await state.close();
  }
});

test("章节跳转目录失败和身份缺失不离开原稿，可原地重试且来源编辑不显示跳转", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, true);
  try {
    const panel = page.getByRole("region", { name: "章节跳转", exact: true });
    await expect(
      panel.getByRole("button", { name: "下一章", exact: true }),
    ).toBeEnabled();
    state.setChapters({ error: "目录暂不可读" }, 503);
    await panel
      .getByRole("button", { name: "刷新章节顺序", exact: true })
      .click();
    await expect(panel).toContainText("目录暂不可读");
    await expect(
      panel.getByRole("button", { name: "下一章", exact: true }),
    ).toBeDisabled();
    state.setChapters({
      items: [{ id: "other", title: "别的章", ready: true, reason: "" }],
    });
    await panel
      .getByRole("button", { name: "刷新章节顺序", exact: true })
      .click();
    await expect(panel).toContainText("当前章已不在最新规划目录");
    await expect(
      panel.getByRole("button", { name: "打开所选章", exact: true }),
    ).toBeDisabled();
    state.setChapters({
      items: [
        { id: "chapter", title: "第一章", ready: true, reason: "" },
        { id: "chapter", title: "重复身份", ready: true, reason: "" },
      ],
    });
    await panel
      .getByRole("button", { name: "刷新章节顺序", exact: true })
      .click();
    await expect(panel).toContainText("章节目录格式或身份不完整");
    state.setChapters({ error: "当前权限不能读取章节" }, 403);
    await panel
      .getByRole("button", { name: "刷新章节顺序", exact: true })
      .click();
    await expect(panel).toContainText("当前权限不能读取章节");
    await expect(panel.getByRole("option")).toHaveCount(1);
    await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
      original,
    );
    expect(state.writes).toHaveLength(0);
  } finally {
    await state.close();
  }
  const review = await fixture(page, info, original, true, true);
  try {
    await expect(
      page.getByRole("region", { name: "章节跳转", exact: true }),
    ).toHaveCount(0);
    expect(
      review.calls.some((call) =>
        call.path.endsWith("/writing-batches/chapters"),
      ),
    ).toBe(false);
  } finally {
    await review.close();
  }
});

test("章内查找精确定位重复词，预览后替换单处并沿原保存撤销", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    const panel = await tools(page);
    await panel.getByLabel("查找原文", { exact: true }).fill("林舟");
    await expect(panel).toContainText("共 3 处");
    await panel.getByRole("button", { name: "下一处", exact: true }).click();
    const field = page.getByLabel("章节正文", { exact: true });
    expect(
      await field.evaluate((e: HTMLTextAreaElement) => [
        e.selectionStart,
        e.selectionEnd,
      ]),
    ).toEqual([6, 8]);
    await panel.getByLabel("替换为", { exact: true }).fill("$&·陆舟");
    await panel
      .getByRole("button", { name: "预览替换当前处", exact: true })
      .click();
    await expect(panel.getByRole("region", { name: "替换预览" })).toContainText(
      "$&·陆舟",
    );
    expect(state.writes).toHaveLength(0);
    await panel
      .getByRole("button", { name: "确认应用替换", exact: true })
      .click();
    const changed = "🌙林舟问：$&·陆舟？\n林舟与临舟。$& [门]";
    await expect(field).toHaveValue(changed);
    await expect.poll(() => state.writes.length).toBe(1);
    expect(state.writes[0]).toEqual({ ...state.original, text: changed });
    await expect(
      panel.getByRole("button", { name: "撤销本次替换", exact: true }),
    ).toBeEnabled();
    await panel
      .getByRole("button", { name: "撤销本次替换", exact: true })
      .click();
    await expect(field).toHaveValue(original);
    await expect.poll(() => state.writes.length).toBe(2);
    expect(state.writes[1]).toEqual(state.original);
    expect(state.calls.filter((c) => c.method === "POST")).toEqual([
      { path: "/api/novels/book/chapters", method: "POST" },
    ]);
  } finally {
    await state.close();
  }
});

test("本章全部替换和删除须预览确认，后续手改使预览及撤销失效", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    const panel = await tools(page);
    const field = page.getByLabel("章节正文", { exact: true });
    await panel.getByLabel("查找原文", { exact: true }).fill("林舟");
    await panel.getByLabel("替换为", { exact: true }).fill("陆舟");
    await panel
      .getByRole("button", { name: "预览替换本章全部", exact: true })
      .click();
    await expect(panel.getByRole("region", { name: "替换预览" })).toContainText(
      "3 处",
    );
    await field.fill(original + "\n作者补充。");
    await expect(
      panel.getByRole("button", { name: "确认应用替换", exact: true }),
    ).toHaveCount(0);
    await panel
      .getByRole("button", { name: "预览替换本章全部", exact: true })
      .click();
    await panel.getByLabel("替换为", { exact: true }).fill("");
    await expect(
      panel.getByRole("button", { name: "确认应用替换", exact: true }),
    ).toHaveCount(0);
    await panel
      .getByRole("button", { name: "预览替换本章全部", exact: true })
      .click();
    await expect(panel.getByRole("region", { name: "替换预览" })).toContainText(
      "删除",
    );
    await panel
      .getByRole("button", { name: "确认应用替换", exact: true })
      .click();
    await expect(field).toHaveValue("🌙问：？\n与临舟。$& [门]\n作者补充。");
    await field.fill("后续手改。");
    await expect(
      panel.getByRole("button", { name: "撤销本次替换", exact: true }),
    ).toHaveCount(0);
  } finally {
    await state.close();
  }
});

test("专注模式保留正文和保存状态，辅助面板不卸载，窄屏可编辑", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    const field = page.getByLabel("章节正文", { exact: true });
    await page.getByText("本章写作卡与承接", { exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "夜访", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "进入专注写作", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "退出专注写作", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("本章写作卡与承接", { exact: true }),
    ).toBeHidden();
    await field.fill(original + "\n继续写作。");
    await expect.poll(() => state.writes.length).toBe(1);
    await expect(
      page.getByText("私人稿已暂存", { exact: false }),
    ).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page
      .getByRole("button", { name: "退出专注写作", exact: true })
      .click();
    await expect(field).toHaveValue(original + "\n继续写作。");
    await expect(
      page.getByText("本章写作卡与承接", { exact: true }),
    ).toBeVisible();
    expect(
      state.calls.filter((c) => c.path.endsWith("/writing-card")),
    ).toHaveLength(1);
    expect(
      state.calls.some((c) => /generate|writer|prepare|start/.test(c.path)),
    ).toBe(false);
  } finally {
    await state.close();
  }
});

test("保存结果未知期间不能应用替换，零命中与过多命中不产生部分替换", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "甲".repeat(5001));
  try {
    const panel = await tools(page);
    await panel.getByLabel("查找原文", { exact: true }).fill("甲");
    await panel.getByLabel("替换为", { exact: true }).fill("乙");
    await expect(panel).toContainText("共 5001 处");
    await expect(
      panel.getByRole("button", { name: "预览替换本章全部", exact: true }),
    ).toBeDisabled();
    await panel.getByLabel("查找原文", { exact: true }).fill("丙");
    await expect(panel).toContainText("共 0 处");
    await expect(
      panel.getByRole("button", { name: "预览替换当前处", exact: true }),
    ).toBeDisabled();
    state.hold();
    await page.getByLabel("章节正文", { exact: true }).fill("丙甲");
    await expect.poll(() => state.writes.length).toBe(1);
    await expect(
      panel.getByRole("button", { name: "预览替换当前处", exact: true }),
    ).toBeDisabled();
    state.release();
    await expect(
      panel.getByRole("button", { name: "预览替换当前处", exact: true }),
    ).toBeEnabled();
    expect(state.writes).toHaveLength(1);
  } finally {
    await state.close();
  }
});

test("位置与其他章稿字段变化清除旧预览，后续上下文变化不能撤销旧替换", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    const panel = await tools(page);
    await panel.getByLabel("查找原文", { exact: true }).fill("林舟");
    await panel.getByLabel("替换为", { exact: true }).fill("陆舟");
    await panel
      .getByRole("button", { name: "预览替换当前处", exact: true })
      .click();
    await panel.getByRole("button", { name: "下一处", exact: true }).click();
    await expect(
      panel.getByRole("button", { name: "确认应用替换", exact: true }),
    ).toHaveCount(0);
    await panel
      .getByRole("button", { name: "预览替换当前处", exact: true })
      .click();
    await page.getByLabel("章节标题", { exact: true }).fill("新的章名");
    await expect(
      panel.getByRole("button", { name: "确认应用替换", exact: true }),
    ).toHaveCount(0);
    await expect.poll(() => state.writes.length).toBe(1);
    await expect(
      panel.getByRole("button", { name: "预览替换当前处", exact: true }),
    ).toBeEnabled();
    await panel
      .getByRole("button", { name: "预览替换当前处", exact: true })
      .click();
    await panel
      .getByRole("button", { name: "确认应用替换", exact: true })
      .click();
    await expect(
      panel.getByRole("button", { name: "撤销本次替换", exact: true }),
    ).toBeVisible();
    await page.getByLabel("故事位置", { exact: true }).fill("13");
    await expect(
      panel.getByRole("button", { name: "撤销本次替换", exact: true }),
    ).toHaveCount(0);
  } finally {
    await state.close();
  }
});

test("精确匹配和替换的 Unicode、换行、边界与容量契约", () => {
  expect(findChapterMatches("🌙林舟\r\n林舟", "林舟")).toEqual({
    total: 2,
    matches: [
      { start: 2, end: 4 },
      { start: 6, end: 8 },
    ],
    truncated: false,
  });
  expect(findChapterMatches("aaaa", "aa").matches).toEqual([
    { start: 0, end: 2 },
    { start: 2, end: 4 },
  ]);
  expect(findChapterMatches("林舟林舟", "").total).toBe(0);
  expect(findChapterMatches("é", "e\u0301").total).toBe(0);
  expect(findChapterMatches("aA", "A").total).toBe(1);
  expect(
    previewChapterReplacement("a[a]\r\na[a]", "[a]", "$&", "all").text,
  ).toBe("a$&\r\na$&");
  expect(previewChapterReplacement("甲甲甲", "甲", "", "one", 1).text).toBe(
    "甲甲",
  );
  expect(previewChapterReplacement("🌙🌙", "🌙", "星", "one", 1).text).toBe(
    "🌙星",
  );
  expect(() => previewChapterReplacement("甲", "甲", "甲", "all")).toThrow(
    /相同/,
  );
  expect(() => previewChapterReplacement("甲", "甲", "乙", "one", -1)).toThrow(
    /位置/,
  );
  expect(() => findChapterMatches("🌙", "\ud83c")).toThrow(/不完整/);
  expect(() => previewChapterReplacement("甲", "甲", "\udc00", "one")).toThrow(
    /不完整/,
  );
  expect(findChapterMatches("甲".repeat(5001), "甲")).toMatchObject({
    total: 5001,
    truncated: true,
  });
  expect(() =>
    previewChapterReplacement("甲".repeat(5001), "甲", "乙", "all"),
  ).toThrow(/上限/);
  expect(
    previewChapterReplacement("甲".repeat(5001), "甲", "乙", "one", 4999)
      .changes,
  ).toHaveLength(1);
  expect(() =>
    previewChapterReplacement(
      "甲".repeat(5000),
      "甲",
      "乙".repeat(100000),
      "all",
    ),
  ).toThrow(/100000/);
  expect(
    previewChapterReplacement(
      "甲".repeat(100000),
      "甲甲甲甲甲甲甲甲甲甲甲甲甲甲甲甲甲甲甲甲",
      "乙".repeat(20),
      "all",
    ).text,
  ).toHaveLength(100000);
});

test("CRLF 章稿定位转换到 textarea 选区，替换仍保留原始换行", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "🌙林舟\r\n林舟结尾");
  try {
    const panel = await tools(page);
    await panel.getByLabel("查找原文", { exact: true }).fill("林舟");
    await panel.getByRole("button", { name: "下一处", exact: true }).click();
    const field = page.getByLabel("章节正文", { exact: true });
    expect(
      await field.evaluate((e: HTMLTextAreaElement) => [
        e.selectionStart,
        e.selectionEnd,
        e.value.slice(e.selectionStart, e.selectionEnd),
      ]),
    ).toEqual([5, 7, "林舟"]);
    await panel.getByLabel("替换为", { exact: true }).fill("陆舟");
    await panel
      .getByRole("button", { name: "预览替换当前处", exact: true })
      .click();
    await panel
      .getByRole("button", { name: "确认应用替换", exact: true })
      .click();
    await expect.poll(() => state.writes.length).toBe(1);
    expect(state.writes[0].text).toBe("🌙林舟\r\n陆舟结尾");
  } finally {
    await state.close();
  }
});

test("保存响应丢失保持未知，先只读核对已提交稿，不重发或覆盖继续手写", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    const panel = await tools(page);
    await panel.getByLabel("查找原文", { exact: true }).fill("林舟");
    await panel.getByLabel("替换为", { exact: true }).fill("陆舟");
    state.loseSave();
    const field = page.getByLabel("章节正文", { exact: true });
    await field.fill(original + "\n首次手改。");
    await expect.poll(() => state.writes.length).toBe(1);
    await expect(
      panel.getByRole("button", { name: "预览替换当前处", exact: true }),
    ).toBeDisabled();
    const recovery = page.getByRole("region", {
      name: "章稿保存核对",
      exact: true,
    });
    await expect(recovery).toBeVisible();
    await field.fill(original + "\n首次手改。继续补充。");
    await recovery
      .getByRole("button", { name: "核对上次保存", exact: true })
      .click();
    await expect(recovery).toHaveCount(0);
    await expect(field).toHaveValue(original + "\n首次手改。继续补充。");
    await expect(
      panel.getByRole("button", { name: "预览替换当前处", exact: true }),
    ).toBeEnabled();
    expect(state.writes).toHaveLength(1);
    await page.getByRole("button", { name: "保存私人稿", exact: true }).click();
    await expect.poll(() => state.writes.length).toBe(2);
    expect(state.writes[1].text).toBe(original + "\n首次手改。继续补充。");
  } finally {
    await state.close();
  }
});

test("读取仍是旧稿时经差异确认再保存，继续手写使旧确认失效", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    state.loseSave(false);
    const field = page.getByLabel("章节正文", { exact: true });
    await field.fill(original + "\n本页新稿。");
    const recovery = page.getByRole("region", {
      name: "章稿保存核对",
      exact: true,
    });
    await expect(recovery).toBeVisible();
    await recovery
      .getByRole("button", { name: "核对上次保存", exact: true })
      .click();
    await expect(
      recovery.getByRole("region", { name: "正文差异对照" }),
    ).toBeVisible();
    expect(state.writes).toHaveLength(1);
    await recovery
      .getByRole("checkbox", {
        name: "我已核对，以刚读取的版本为基础保存本页稿",
        exact: true,
      })
      .check();
    await field.fill(original + "\n本页新稿。继续补充。");
    await expect(
      recovery.getByRole("button", {
        name: "按已读版本保存本页稿",
        exact: true,
      }),
    ).toHaveCount(0);
    await recovery
      .getByRole("button", { name: "核对上次保存", exact: true })
      .click();
    await expect(
      recovery.getByRole("button", {
        name: "按已读版本保存本页稿",
        exact: true,
      }),
    ).toBeDisabled();
    await recovery
      .getByRole("checkbox", {
        name: "我已核对，以刚读取的版本为基础保存本页稿",
        exact: true,
      })
      .check();
    await recovery
      .getByRole("button", { name: "按已读版本保存本页稿", exact: true })
      .click();
    await expect.poll(() => state.writes.length).toBe(2);
    await expect(recovery).toHaveCount(0);
    expect(state.writes[1].text).toBe(original + "\n本页新稿。继续补充。");
  } finally {
    await state.close();
  }
});

test("换行选区双向映射及保存核对仅接受确切下一版或原基础", () => {
  const raw = "🌙甲\r\n乙\r丙\n丁";
  expect(chapterTextareaSelection(raw, 5, 6)).toMatchObject({
    start: 4,
    end: 5,
  });
  expect(chapterSourceSelection(raw, 4, 5)).toEqual({ start: 5, end: 6 });
  expect(chapterTextareaSelection(raw, 4, 4)).toMatchObject({
    start: 3,
    end: 3,
  });
  expect(() => chapterSourceSelection(raw, 1, 2)).toThrow(/完整字符/);
  const previous = {
    schema: 1,
    chapter_id: "chapter",
    body_id: "body",
    title: "原章",
    text: "原稿",
    detail: {
      kind: "resource",
      id: "detail",
      branch_id: "branch",
      revision: "head",
      hash: "basis",
    },
    settings_revision: 1,
    settings_hash: "settings",
    ip_head: "",
    story_time: 1,
    perspective: "",
  } as ChapterDraft["document"];
  const document = { ...previous, text: "新稿" };
  const attempt = {
    id: 1,
    resourceId: "control",
    revision: 1,
    hash: "old-hash",
    baseHead: "head",
    previous,
    document,
    content: JSON.stringify(document),
  };
  const remote = {
    resource: { id: "control" },
    revision: 2,
    hash: "new-hash",
    base_head: "head",
    document: { ...document, references: [], writer_generation_id: "" },
    ready: true,
    reason: "",
  } as ChapterDraft;
  expect(sameChapterDocument(document, remote.document)).toBe(true);
  expect(classifyChapterSave(attempt, remote)).toBe("committed");
  expect(
    classifyChapterSave(attempt, {
      ...remote,
      revision: 1,
      hash: "old-hash",
      document: previous,
    }),
  ).toBe("unchanged");
  expect(classifyChapterSave(attempt, { ...remote, revision: 3 })).toBe(
    "diverged",
  );
  expect(
    classifyChapterSave(attempt, {
      ...remote,
      document: { ...document, text: "其他页面的新稿" },
    }),
  ).toBe("diverged");
  expect(
    classifyChapterSave(attempt, { ...remote, base_head: "other-head" }),
  ).toBe("diverged");
  expect(
    classifyChapterSave(attempt, {
      ...remote,
      resource: { ...remote.resource, id: "other" },
    }),
  ).toBe("diverged");
});

test("留版响应悬挂转为待核对，不取消或重发写请求", async ({ page }, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    await page.clock.install();
    await page
      .getByRole("button", { name: "章节修订历史", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "章节修订历史",
      exact: true,
    });
    state.holdCheckpoint();
    await panel
      .getByRole("button", { name: "保存当前版本", exact: true })
      .click();
    await expect.poll(() => state.history.length).toBe(1);
    await page.clock.runFor(15010);
    await expect(
      panel.getByRole("button", { name: "核对留版结果", exact: true }),
    ).toBeEnabled();
    await panel
      .getByRole("button", { name: "核对留版结果", exact: true })
      .click();
    await expect(panel.getByText("版本已留存", { exact: false })).toBeVisible();
    expect(
      state.calls.filter(
        (v) => v.path.endsWith("/history") && v.method === "POST",
      ),
    ).toHaveLength(1);
    expect(state.checkpointReplies()).toBe(0);
    state.release();
    await expect.poll(() => state.checkpointReplies()).toBe(1);
  } finally {
    await state.close();
  }
});

test("权限拒绝后迟到留版和核对回执不能复原受保护历史", async ({
  page,
}, info) => {
  const state = await fixture(page, info, original, false, false, true);
  try {
    await page
      .getByRole("button", { name: "章节修订历史", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "章节修订历史",
      exact: true,
    });
    const note = panel.getByLabel("版本备注", { exact: true });
    for (const mode of ["write", "read"]) {
      await note.fill("权限撤销前尚未确认的备注");
      if (mode === "write") state.holdCheckpoint();
      else state.loseCheckpoint();
      await panel
        .getByRole("button", { name: "保存当前版本", exact: true })
        .click();
      await expect
        .poll(() => state.history.length)
        .toBe(mode === "write" ? 1 : 2);
      if (mode === "read") {
        state.holdOperation();
        await panel
          .getByRole("button", { name: "核对留版结果", exact: true })
          .click();
        await expect.poll(() => state.operationHolds()).toBe(1);
      }
      state.historyStatus(403);
      await panel
        .getByRole("button", { name: "刷新修订历史", exact: true })
        .click();
      await expect(panel.getByRole("alert")).toContainText("权限");
      state.historyStatus(200);
      state.release();
      await expect
        .poll(() => state.checkpointReplies())
        .toBe(mode === "write" ? 1 : 2);
      await page.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
      );
      await expect(note).toHaveValue("权限撤销前尚未确认的备注");
      await expect(
        panel.getByLabel("历史版本", { exact: true }).locator("option"),
      ).toHaveCount(1);
      await panel
        .getByRole("button", { name: "核对留版结果", exact: true })
        .click();
      await expect(
        panel.getByText("版本已留存", { exact: false }),
      ).toBeVisible();
    }
  } finally {
    await state.close();
  }
});
