import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";

const quote = "旧约尚未兑现。";
function entry(
  id: string,
  person: string,
  kind = "promise",
  storyTime = 7,
  context = "reality",
) {
  return {
    id,
    kind,
    context,
    subject_id: person,
    subject_name: "同名人物",
    holder_id: "",
    object_id: "",
    predicate: "约定",
    statement: `${id}：归还钥匙`,
    story_time: storyTime,
    known_from: storyTime + 1,
    visibility: "author",
    known_by: [],
    quotes: [quote],
    evidence: [{ start_rune: 3, end_rune: 11, quote }],
  };
}
function item(id: string, source = "private", matched = true) {
  return {
    chapter_id: `chapter-${id}`,
    control_id: `control-${id}`,
    ledger_id: `ledger-${id}`,
    chapter_title: "重逢",
    origin: source,
    ledger_revision: 1,
    ledger_hash: `ledger-hash-${id}`,
    source_hash: `source-${id}`,
    current_hash: matched ? `source-${id}` : `changed-${id}`,
    current_revision: matched ? 1 : 2,
    current_ready: true,
    source_matches_current: matched,
    reason: matched ? "" : "正文已改变，请核对旧账本",
    summary: `本章概括${id}`,
    entries: [entry("same-entry", id === "a" ? "person-a" : "person-b")],
    people: [{ id: id === "a" ? "person-a" : "person-b", name: "同名人物" }],
  };
}
async function fixture(
  page: Page,
  info: TestInfo,
  workspace = false,
  options: { noPrivate?: boolean; large?: boolean } = {},
) {
  const path = `/src/features/novel/${workspace ? "NovelWorkspace" : "NovelStoryLedger"}.vue`;
  const compiled = await (await page.request.get(path)).text();
  const vue = compiled.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  const counts = {
    reads: [] as string[],
    writes: [] as string[],
    saves: 0,
    ledgerSaves: 0,
    closed: 0,
  };
  let mode:
    | "normal"
    | "hold"
    | "fail"
    | "deny"
    | "bad"
    | "basis"
    | "unavailable" = "normal";
  let text = `🌙开头。${quote}落幕。`,
    revision = options.noPrivate ? 0 : 1,
    ledgerRevision = options.noPrivate ? 0 : 1;
  const held: (() => void)[] = [];
  const draft = () => ({
    resource: {
      id: "control-a",
      kind: "novel_chapter_control",
      name: "重逢",
      head: "",
      revision: 1,
    },
    revision,
    hash: revision <= 1 ? "source-a" : "changed-a",
    base_head: "",
    ready: true,
    reason: "",
    document: {
      schema: 1,
      chapter_id: "chapter-a",
      body_id: "body-a",
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
  const ledger = () => ({
    resource: {
      id: "ledger-a",
      kind: "novel_chapter_ledger",
      head: "",
      revision: 1,
    },
    revision: ledgerRevision,
    base_head: "",
    hash: ledgerRevision === 1 ? "ledger-hash-a" : "new-ledger",
    ready: !options.noPrivate,
    reason: options.noPrivate ? "正式账本来源版本需要重新核对" : "",
    document: {
      schema: 1,
      source: {
        book_id: "book",
        resource_id: "control-a",
        revision,
        hash: draft().hash,
        base_head: "",
      },
      chapter_id: "chapter-a",
      body_id: "body-a",
      text_hash: "text-hash",
      summary: "本章概括a",
      entries: [
        entry("earlier", "person-a", "event"),
        entry("same-entry", "person-a"),
      ],
      notes: [],
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
        .end(`<!doctype html><meta charset="utf-8"><button id="leave">离开</button><output id="target"></output><div id="app"></div><script type="module">
import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};
import Component from ${JSON.stringify(vite + path)};
const book={id:'book',project_id:'project',branch_id:'branch',settings_revision:1,stage:'WORLD_PLANNING',setup:{title:'雾城',genre:'悬疑',language:'简体中文',idea:'调查旧案',conflict:'旧约',target_words:300000,volume_count:3,chapter_min:2000,chapter_max:4000,characters:[]}};
window.expired=0;const app=createApp({render:()=>h(Component,{book,bookId:'book',project:{id:'project',name:'雾城',role:'owner',revision:1},actor:{id:'owner',workspace_id:'workspace',username:'owner',role:'owner'},onEdit:target=>document.querySelector('#target').textContent=JSON.stringify(target),onExpired:()=>window.expired++})});app.mount('#app');document.querySelector('#leave').onclick=()=>app.unmount();</script>`);
    let body = "";
    request.on("data", (chunk) => (body += chunk));
    request.on("end", () => {
      const input = body ? JSON.parse(body) : {};
      if (url.pathname.endsWith("/story-ledger")) {
        counts.reads.push(url.search);
        const deliver = () => {
          const a = item(
            "a",
            url.searchParams.get("source") === "published" || options.noPrivate
              ? "published"
              : "private",
            revision <= 1,
          );
          const b = item("b", "published", false);
          if (mode === "basis") {
            a.current_ready = false;
            a.reason = "写作依据已变化，请重新核对";
          }
          if (mode === "unavailable") {
            a.origin = "unavailable";
            a.entries = [];
            a.reason = "原账本来源不可读";
            a.source_matches_current = false;
          }
          b.entries = [entry("same-entry", "person-b", "belief", 2, "dream")];
          if (options.large && url.searchParams.get("source") !== "published") {
            a.entries = Array.from({ length: 60 }, (_, index) =>
              entry(`a-${index}`, "person-a"),
            );
            b.entries = Array.from({ length: 60 }, (_, index) =>
              entry(`b-${index}`, "person-b"),
            );
          }
          return json({
            book_id: mode === "bad" ? "wrong" : "book",
            source: url.searchParams.get("source") ?? "working",
            items: url.searchParams.get("after") ? [b] : [a],
            next_cursor: url.searchParams.get("after") ? "" : "ledger-a",
            scanned: 1,
            notes: ["只读取已保存账本，不判断伏笔是否回收"],
          });
        };
        if (mode === "hold") {
          held.push(deliver);
          response.on("close", () => counts.closed++);
          return;
        }
        if (mode === "fail") return json({ error: "暂时读取失败" }, 503);
        if (mode === "deny") return json({ error: "当前权限已撤回" }, 403);
        return deliver();
      }
      if (url.pathname.endsWith("/plans"))
        return json({ stage: "WORLD_PLANNING", resources: [], sources: [] });
      if (url.pathname.endsWith("/writing-batches/chapters"))
        return json({ items: [] });
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
      if (url.pathname.endsWith("/branches/branch"))
        return json({ id: "branch", head: "head", snapshot: { entities: [] } });
      if (url.pathname.endsWith("/draft")) {
        if (request.method === "PUT") {
          text = input.document.text;
          revision++;
          counts.saves++;
          counts.writes.push(url.pathname);
        }
        return json(draft());
      }
      if (url.pathname.endsWith("/ledger")) {
        if (request.method !== "GET") counts.writes.push(url.pathname);
        if (request.method === "PUT") ledgerRevision++;
        if (request.method === "PUT" && options.noPrivate)
          return json({ error: "旧账本来源版本不能沿用" }, 409);
        return json(ledger());
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
    mode: (value: typeof mode) => (mode = value),
    release: () => held.splice(0).forEach((fn) => fn()),
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

test("全书账本按人物ID与语境筛选并分页，读取与证据目标不产生写入", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    const desk = page.getByRole("region", { name: "全书剧情账本" });
    await expect(
      desk.getByText("same-entry：归还钥匙", { exact: true }),
    ).toBeVisible();
    await desk
      .getByRole("button", { name: "继续读取账本", exact: true })
      .click();
    await expect(
      desk.getByRole("button", { name: "查看条目 ledger-b same-entry" }),
    ).toBeVisible();
    await desk.getByLabel("关联人物").selectOption("person-b");
    await expect(
      desk.getByRole("button", { name: "查看条目 ledger-a same-entry" }),
    ).toHaveCount(0);
    await desk
      .getByRole("button", { name: "查看条目 ledger-b same-entry" })
      .click();
    await expect(desk.getByLabel("所选账本条目")).toContainText("梦境");
    await expect(desk.getByLabel("所选账本条目")).toContainText("正文已改变");
    await desk.getByLabel("关联人物").selectOption("");
    await desk
      .getByRole("button", { name: "查看条目 ledger-a same-entry" })
      .click();
    await desk.getByRole("button", { name: "定位引文并打开章稿 1" }).click();
    await expect(page.locator("#target")).toContainText(
      '"control_id":"control-a"',
    );
    await expect(page.locator("#target")).toContainText(
      '"reviewed_hash":"source-a"',
    );
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("从全书证据回章稿，保存后返回原筛选和条目并重读当前状态", async ({
  page,
}, info) => {
  const f = await fixture(page, info, true);
  try {
    await page.getByText("全部创作工具", { exact: true }).click();
    await page
      .getByRole("button", { name: "全书剧情账本", exact: true })
      .click();
    await page.getByLabel("搜索已加载账本").fill("归还钥匙");
    await page
      .getByRole("button", { name: "查看条目 ledger-a same-entry" })
      .click();
    await page.getByRole("button", { name: "定位引文并打开章稿 1" }).click();
    await expect(page.getByRole("region", { name: "章节写作" })).toBeVisible();
    await expect(
      page.getByRole("region", { name: "账本证据", exact: true }),
    ).toContainText(quote);
    await page
      .getByLabel("章节正文", { exact: true })
      .fill(`修改后的正文。${quote}`);
    await page
      .getByRole("button", { name: "保存并返回剧情账本", exact: true })
      .click();
    await expect(page.getByLabel("搜索已加载账本")).toHaveValue("归还钥匙");
    await expect(page.getByLabel("所选账本条目")).toContainText("正文已改变");
    expect(f.counts.saves).toBe(1);
    expect(f.counts.writes.every((path) => path.endsWith("/draft"))).toBe(true);
    expect(f.counts.reads.length).toBeGreaterThan(1);
  } finally {
    await f.close();
  }
});

test("直接打开指定账本定位条目，保存返回原视图且不创建账本或发起生成", async ({
  page,
}, info) => {
  const f = await fixture(page, info, true);
  try {
    await page.getByText("全部创作工具", { exact: true }).click();
    await page
      .getByRole("button", { name: "全书剧情账本", exact: true })
      .click();
    await page
      .getByRole("button", { name: "查看条目 ledger-a same-entry" })
      .click();
    await page
      .getByRole("button", { name: "打开对应账本", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "本章剧情账本" }),
    ).toBeVisible();
    await expect(
      page.locator('[data-ledger-entry-id="same-entry"]'),
    ).toHaveClass(/ledger-entry-focused/);
    await page
      .getByRole("button", { name: "保存账本并返回剧情账本", exact: true })
      .click();
    await expect(page.getByLabel("所选账本条目")).toBeVisible();
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("读取超时和临时失败可恢复，换来源不接受迟到结果，当前拒绝清理受保护内容", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    const desk = page.getByRole("region", { name: "全书剧情账本" });
    await desk.getByLabel("搜索已加载账本").fill("归还");
    f.mode("hold");
    await desk.getByRole("button", { name: "刷新账本", exact: true }).click();
    await page.clock.runFor(16000);
    await expect(desk.getByRole("alert")).toContainText("超时");
    await expect(desk.getByLabel("搜索已加载账本")).toHaveValue("归还");
    f.mode("normal");
    await desk.getByRole("button", { name: "刷新账本", exact: true }).click();
    await expect(
      desk.getByRole("button", { name: "查看条目 ledger-a same-entry" }),
    ).toBeVisible();
    f.mode("hold");
    await desk
      .getByRole("button", { name: "继续读取账本", exact: true })
      .click();
    f.mode("normal");
    await desk.getByLabel("账本来源").selectOption("published");
    f.release();
    await expect(
      desk.getByRole("status").filter({ hasText: "已加载" }),
    ).toContainText("1 本账本");
    f.mode("deny");
    await desk.getByRole("button", { name: "刷新账本", exact: true }).click();
    await expect(desk.getByRole("button", { name: /查看条目/ })).toHaveCount(0);
    await expect(desk.getByLabel("关联人物").locator("option")).toHaveCount(1);
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("连续读取止于分页尾，错误书籍响应拒绝且不显示部分错误结果", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    const desk = page.getByRole("region", { name: "全书剧情账本" });
    await desk
      .getByRole("button", { name: "读取其余账本", exact: true })
      .click();
    await expect(
      desk.getByRole("button", { name: "查看条目 ledger-b same-entry" }),
    ).toBeVisible();
    expect(f.counts.reads).toHaveLength(2);
    f.mode("bad");
    await desk.getByRole("button", { name: "刷新账本", exact: true }).click();
    await expect(desk.getByRole("alert")).toContainText("不一致");
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("停止连续读取不加入迟到页，卸载取消所属GET且不会写入", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    const desk = page.getByRole("region", { name: "全书剧情账本" });
    f.mode("hold");
    await desk
      .getByRole("button", { name: "读取其余账本", exact: true })
      .click();
    await desk
      .getByRole("button", { name: "停止继续读取", exact: true })
      .click();
    f.mode("normal");
    f.release();
    await expect(
      desk.getByRole("button", { name: "查看条目 ledger-b same-entry" }),
    ).toHaveCount(0);
    expect(f.counts.reads).toHaveLength(2);
    f.mode("hold");
    await desk
      .getByRole("button", { name: "继续读取账本", exact: true })
      .click();
    await expect.poll(() => f.counts.reads.length).toBe(3);
    await page.locator("#leave").click();
    await expect.poll(() => f.counts.closed).toBeGreaterThan(0);
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("缺失来源显示覆盖限制，依据失效与原章稿一致分别展示", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    const desk = page.getByRole("region", { name: "全书剧情账本" });
    await expect(
      desk.getByRole("button", { name: "查看条目 ledger-a same-entry" }),
    ).toBeVisible();
    f.mode("unavailable");
    await desk.getByRole("button", { name: "刷新账本", exact: true }).click();
    await expect(desk.getByLabel("未展示条目的账本")).toContainText(
      "原账本来源不可读",
    );
    await desk
      .getByRole("button", { name: "继续读取账本", exact: true })
      .click();
    await expect(
      desk.getByRole("button", { name: "查看条目 ledger-b same-entry" }),
    ).toBeVisible();
    f.mode("basis");
    await desk.getByRole("button", { name: "刷新账本", exact: true }).click();
    await desk
      .getByRole("button", { name: "查看条目 ledger-a same-entry" })
      .click();
    await expect(desk.getByLabel("所选账本条目")).toContainText(
      "原账本引用的章稿与当前已保存章稿一致",
    );
    await expect(desk.getByLabel("所选账本条目")).toContainText(
      "当前章稿写作依据需要重新核对",
    );
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("无本人私稿的正式账本仅回查即可返回，不因revision零而创建或保存副本", async ({
  page,
}, info) => {
  const f = await fixture(page, info, true, { noPrivate: true });
  try {
    await page.getByText("全部创作工具", { exact: true }).click();
    await page
      .getByRole("button", { name: "全书剧情账本", exact: true })
      .click();
    await page
      .getByRole("button", { name: "查看条目 ledger-a same-entry" })
      .click();
    await page
      .getByRole("button", { name: "打开对应账本", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "本章剧情账本" }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "保存账本并返回剧情账本", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "全书剧情账本", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "定位引文并打开章稿 1" }).click();
    await page
      .getByRole("button", { name: "保存并返回剧情账本", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "全书剧情账本", exact: true }),
    ).toBeVisible();
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});

test("翻到后页再切换到更少的正式条目时重置分页，所有结果仍可见", async ({
  page,
}, info) => {
  const f = await fixture(page, info, false, { large: true });
  try {
    const desk = page.getByRole("region", { name: "全书剧情账本" });
    await desk
      .getByRole("button", { name: "继续读取账本", exact: true })
      .click();
    await desk
      .getByRole("navigation", { name: "已加载条目分页" })
      .getByRole("button", { name: "下一页", exact: true })
      .click();
    await desk
      .getByRole("navigation", { name: "已加载条目分页" })
      .getByRole("button", { name: "下一页", exact: true })
      .click();
    await expect(
      desk.getByRole("navigation", { name: "已加载条目分页" }),
    ).toContainText("第 3 / 3 页");
    await desk.getByLabel("账本来源").selectOption("published");
    await expect(
      desk.getByRole("button", { name: "查看条目 ledger-a same-entry" }),
    ).toBeVisible();
    expect(f.counts.writes).toHaveLength(0);
  } finally {
    await f.close();
  }
});
