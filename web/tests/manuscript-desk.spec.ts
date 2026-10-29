import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";
import type { ManuscriptChapter } from "../src/features/novel/manuscriptTypes";

function chapter(
  id: string,
  values: Partial<ManuscriptChapter> = {},
): ManuscriptChapter {
  return {
    id,
    resource_id: `body-${id}`,
    volume_id: "v1",
    volume: "第一卷",
    title: `章节${id}`,
    hash: `hash-${id}`,
    head: "head",
    warning: "",
    revision: 1,
    characters: 100,
    included: true,
    ...values,
  };
}
const current = [
  chapter("a", {
    title: "雨夜来客",
    revision: 2,
    hash: "new-a",
    characters: 1200,
  }),
  chapter("b", { title: "旧城线索", revision: 0, characters: 800 }),
  chapter("c", {
    title: "旅途",
    volume_id: "v2",
    volume: "第二卷",
    resource_id: "",
    hash: "",
    head: "",
    revision: 0,
    included: false,
    characters: 0,
  }),
  chapter("d", {
    title: "终局",
    volume_id: "v2",
    volume: "第二卷",
    resource_id: "",
    hash: "",
    head: "",
    revision: 0,
    included: false,
    characters: 0,
  }),
];
const published = current.map((c) => ({
  ...c,
  revision: c.included ? 1 : 0,
  hash: c.id === "a" ? "old-a" : c.hash,
  characters: c.id === "a" ? 1000 : c.characters,
}));
const oldEdition = [chapter("a"), chapter("b"), chapter("p"), chapter("z")];
const newEdition = [
  chapter("c"),
  chapter("b", { title: "线索重排", volume_id: "v2", volume: "第二卷" }),
  chapter("a", { hash: "new-same-length" }),
  chapter("p"),
];

async function fixture(
  page: Page,
  info: TestInfo,
  kind: "catalog" | "compare",
  enabled = true,
) {
  const path = `/src/features/novel/${kind === "catalog" ? "NovelChapterCatalog" : "NovelEditionCompare"}.vue`;
  const source = await (await page.request.get(path)).text();
  const vue = source.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  const counts = { previews: 0, readiness: 0, writes: 0, closed: 0 };
  let mode: "normal" | "failure" | "denied" | "mismatch" | "hold" = "normal";
  const held: (() => void)[] = [];
  const server = createServer((request, response) => {
    const url = new URL(request.url!, "http://127.0.0.1");
    const json = (value: unknown, status = 200) =>
      response
        .writeHead(status, { "Content-Type": "application/json" })
        .end(JSON.stringify(value));
    if (url.pathname === "/") {
      response.writeHead(200, { "Content-Type": "text/html; charset=utf-8" })
        .end(`<!doctype html><meta charset="utf-8"><button id="leave">离开测试页</button><output id="opened"></output><div id="app"></div><script type="module">
import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};
import Component from ${JSON.stringify(vite + path)};
window.expiredEvents=0;
const book={id:'book',setup:{language:'简体中文'}};
const editions=[{id:'new',name:'新稿本',title:'新书',published:true},{id:'old',name:'原稿本',title:'旧书',published:true}];
const app=createApp({render:()=>h(Component,{book,bookId:'book',enabled:${enabled},editions,
onChapter:id=>document.querySelector('#opened').textContent=id,onPlanning:()=>document.querySelector('#opened').textContent='planning',onExpired:()=>window.expiredEvents++})});
app.mount('#app');document.querySelector('#leave').onclick=()=>app.unmount();</script>`);
      return;
    }
    if (
      url.pathname.endsWith("/writing-batches/chapters") &&
      request.method === "GET"
    ) {
      counts.readiness++;
      return json({
        items: current.map((c) => ({
          id: c.id,
          title: c.title,
          ready: ["a", "c"].includes(c.id),
          reason: "请确认有效细纲",
        })),
      });
    }
    if (url.pathname.endsWith("/export/preview") && request.method === "POST") {
      counts.previews++;
      let body = "";
      request.on("data", (chunk) => {
        body += chunk;
      });
      request.on("end", () => {
        const input = JSON.parse(body);
        let chapters =
          input.source === "edition"
            ? input.edition_id === "old"
              ? oldEdition
              : newEdition
            : input.source === "draft"
              ? current
              : published;
        if (mode === "mismatch" && input.source === "published")
          chapters = chapters.slice(1);
        const deliver = () =>
          json({
            hash: "snapshot",
            title: "隔离稿本",
            source: input.source,
            ...(input.source === "edition"
              ? { edition_id: input.edition_id }
              : {}),
            chapters,
            included: chapters.filter((c) => c.included).length,
            missing: chapters.filter((c) => !c.included).length,
            warnings: 0,
            characters: chapters.reduce(
              (sum, c) => sum + (c.included ? c.characters : 0),
              0,
            ),
          });
        if (mode === "failure") return json({ error: "暂时读取失败" }, 503);
        if (mode === "denied")
          return input.source === "draft" || input.edition_id === "old"
            ? json({ error: "暂时读取失败" }, 503)
            : json({ error: "没有权限" }, 403);
        if (mode === "hold") {
          held.push(deliver);
          response.on("close", () => counts.closed++);
          return;
        }
        deliver();
      });
      return;
    }
    if (request.method !== "GET") counts.writes++;
    json({ error: "未预期接口" }, 404);
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  await page.clock.install({ time: new Date("2026-10-29T00:00:00Z") });
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  return {
    counts,
    mode: (value: typeof mode) => {
      mode = value;
    },
    release: () => {
      held.splice(0).forEach((fn) => fn());
    },
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

test("全书目录区分私人稿与采用稿，搜索分卷并打开下一待写章；失败恢复不写正文", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "catalog");
  const catalog = page.getByRole("region", { name: "本书章节目录" });
  try {
    await expect(catalog.getByLabel("全书稿件统计")).toContainText(
      "全书 4 章 · 有正文 2 章 · 当前可用稿 2,000 个字符 · 已采用 2 章 · 待核对 1 章",
    );
    await expect(
      catalog.getByText("使用已采用稿", { exact: false }),
    ).toHaveCount(1);
    await page.getByLabel("稿件状态", { exact: true }).selectOption("draft");
    await expect(catalog.getByRole("listitem")).toHaveCount(1);
    await page.getByLabel("稿件状态", { exact: true }).selectOption("all");
    await page.getByLabel("分卷范围").selectOption("v2");
    await expect(catalog.getByRole("listitem")).toHaveCount(2);
    await page.getByRole("button", { name: "打开下一待写章" }).click();
    await expect(page.locator("#opened")).toHaveText("c");
    await page.getByLabel("分卷范围").selectOption("");
    await page.getByRole("searchbox").fill("旧城");
    await page
      .getByRole("button", { name: "打开第2章 · 旧城线索", exact: true })
      .click();
    await expect(page.locator("#opened")).toHaveText("b");
    state.mode("failure");
    await page.getByRole("button", { name: "刷新章节目录" }).click();
    await expect(page.getByRole("alert")).toContainText("保留上次读取结果");
    state.mode("denied");
    await page.getByRole("button", { name: "刷新章节目录" }).click();
    await expect(page.getByRole("alert")).toContainText("无权读取");
    await expect(catalog.getByRole("listitem")).toHaveCount(0);
    state.mode("mismatch");
    await page.getByRole("button", { name: "刷新章节目录" }).click();
    await expect(page.getByRole("alert")).toContainText(
      "目录在读取期间发生变化",
    );
    state.mode("normal");
    await page.getByRole("button", { name: "刷新章节目录" }).click();
    await expect(
      catalog.getByRole("button", {
        name: "打开第2章 · 旧城线索",
        exact: true,
      }),
    ).toBeEnabled();
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
});

test("目录只读超时可重试，离开后取消读取；未完成规划时不读取稿件", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "catalog");
  try {
    await expect(page.getByLabel("全书稿件统计")).toContainText("全书 4 章");
    state.mode("hold");
    await page.getByRole("button", { name: "刷新章节目录" }).click();
    await expect.poll(() => state.counts.previews).toBe(4);
    await page.clock.fastForward(15000);
    await expect(page.getByRole("alert")).toContainText("超时");
    state.mode("normal");
    await page.getByRole("button", { name: "刷新章节目录" }).click();
    await expect(page.getByRole("alert")).toHaveCount(0);
    state.release();
    state.mode("hold");
    await page.getByRole("button", { name: "刷新章节目录" }).click();
    await expect.poll(() => state.counts.previews).toBe(8);
    await page.locator("#leave").click();
    state.release();
    await expect(page.locator("#app")).toBeEmpty();
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
  const offPage = await page.context().newPage();
  const off = await fixture(offPage, info, "catalog", false);
  try {
    await offPage.getByRole("button", { name: "前往章节规划" }).click();
    await expect(offPage.locator("#opened")).toHaveText("planning");
    expect(off.counts.previews).toBe(0);
    expect(off.counts.readiness).toBe(0);
  } finally {
    await off.close();
    await offPage.close();
  }
});

test("两个成书稿本按稳定章节ID比较版本、增删和分卷，字数相同仍标示版本变化", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "compare");
  try {
    expect(state.counts.previews).toBe(0);
    await page.getByRole("button", { name: "比较这两个稿本" }).click();
    const result = page.getByLabel("稿本比较结果");
    await expect(result).toContainText(
      "4 章有变化：新增 1 章，移出 1 章，章节版本变化 1 章，次序变化 2 章",
    );
    await expect(result.getByRole("row")).toHaveCount(5);
    await expect(
      result.getByRole("row").filter({ hasText: "章节a" }),
    ).toContainText("章节版本变化");
    await expect(result).toContainText("字符数相同不能证明内容相同");
    await page.getByRole("checkbox", { name: "仅看变化章节" }).uncheck();
    await expect(result.getByRole("row")).toHaveCount(6);
    await expect(
      result.getByRole("row").filter({ hasText: "章节p" }),
    ).toContainText("无变化");
    state.mode("hold");
    await page.getByRole("button", { name: "比较这两个稿本" }).click();
    await expect.poll(() => state.counts.previews).toBe(4);
    await page.getByLabel("比较原稿本", { exact: true }).selectOption("new");
    state.release();
    await expect(result).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "比较这两个稿本" }),
    ).toBeDisabled();
    await page.getByLabel("比较原稿本", { exact: true }).selectOption("old");
    state.mode("denied");
    await page.getByRole("button", { name: "比较这两个稿本" }).click();
    await expect(page.getByRole("alert")).toContainText("无权读取");
    await expect(result).toHaveCount(0);
    state.mode("normal");
    await page.getByRole("button", { name: "比较这两个稿本" }).click();
    await expect(result).toBeVisible();
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
});
