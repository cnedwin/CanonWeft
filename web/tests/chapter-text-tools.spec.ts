import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";
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
) {
  const vite = new URL(info.project.use.baseURL!).origin;
  const module = "/src/features/novel/NovelChapterEditor.vue";
  const compiled = await (await page.request.get(module)).text();
  const vue = compiled.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const document = {
    schema: 1,
    chapter_id: "chapter",
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
      id: "control",
      branch_id: "branch",
      kind: "novel_chapter_control",
      name: "章节工作稿 · chapter",
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
  const waiting: (() => void)[] = [];
  const server = createServer(async (request, response) => {
    const path = new URL(request.url!, "http://127.0.0.1").pathname;
    calls.push({ path, method: request.method! });
    const json = (value: unknown, status = 200) => {
      response.writeHead(status, { "Content-Type": "application/json" });
      response.end(JSON.stringify(value));
    };
    if (path === "/api/novels/book/writing-batches/chapters")
      return json(chapters, chaptersStatus);
    if (path === "/api/novels/book/chapters" && request.method === "POST")
      return json(draft.resource);
    if (path === "/api/novels/book/chapters/control/draft") {
      if (request.method === "PUT") {
        let body = "";
        for await (const chunk of request) body += chunk;
        const value = JSON.parse(body);
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
  const app=createApp({render:()=>h(Editor,{book:{id:'book',project_id:'project',branch_id:'branch',setup:{title:'雾城',genre:'悬疑',chapter_min:2000,chapter_max:4000}},project:{id:'project',role:'owner'},actor:{id:'author',workspace_id:'workspace',role:'owner'},chapterId:'chapter',allowChapterNavigation:${navigation},reviewTarget:${reviewOrigin ? "{chapter_id:'chapter',control_id:'control',quote:'林舟'}" : "null"},onNavigate:(id)=>window.__chapterNavigations.push(id)})});app.mount('#app');window.__chapterEditApp=app;
  </script></body></html>`,
    }),
  );
  // The real browser uses same-origin requests to the loopback fixture via Vite.
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const response = await page.request.fetch(
      origin + new URL(request.url()).pathname,
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
    original: structuredClone(document),
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
