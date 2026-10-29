import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";

async function fixture(
  page: Page,
  info: TestInfo,
  mode: "initial" | "normal" | "preview" = "normal",
) {
  const component = "/src/features/novel/NovelBatchDesk.vue";
  const source = await (await page.request.get(component)).text();
  const vue = source.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  const root = "/api/projects/project/recipe-executions";
  const book = {
    id: "book",
    branch_id: "branch",
    setup: { title: "隔离连写", genre: "悬疑", chapter_max: 4000 },
  };
  const run = {
    id: "batch-111111",
    branch_id: "branch",
    state: mode === "preview" ? "COMPLETED" : "RUNNING",
    revision: 1,
    paused: false,
    reason: "",
    currency: "CNY",
    budget_limit_micros: 1000000,
    spent_micros: 200000,
    uncertain_calls: 0,
    frozen: {
      input: { book_id: "book", chapter_ids: ["c1"] },
      definition: {
        definition: { key: "novel-batch-fixture", steps: [{ key: "chapter" }] },
      },
    },
    steps:
      mode === "preview"
        ? [
            {
              id: "s1",
              ordinal: 0,
              active: true,
              state: "COMPLETED",
              generation_id: "g1",
              frozen: {
                input: {
                  novel_chapter: { resource_id: "res1", book_id: "book" },
                },
              },
              output: { text: "隔离候选" },
            },
          ]
        : [],
  };
  const other = {
    ...structuredClone(run),
    id: "batch-222222",
    spent_micros: 500000,
  };
  other.frozen.input.chapter_ids = ["c2"];
  const counts = {
    configReads: 0,
    hangs: 0,
    aborted: 0,
    writes: 0,
    writeClosed: 0,
    expired: 0,
  };
  let holdDetail = false,
    unknownWrite = false,
    denyList = false,
    holdPreview = mode === "preview";
  let releaseDenied: (() => void) | undefined;
  let failedPair: "config" | "preview" | undefined;
  const server = createServer((req, res) => {
    const path = new URL(req.url!, "http://127.0.0.1").pathname;
    const json = (value: unknown) =>
      res
        .writeHead(200, { "Content-Type": "application/json" })
        .end(JSON.stringify(value));
    const hang = () => {
      counts.hangs++;
      res.on("close", () => counts.aborted++);
    };
    if (req.method !== "GET") {
      counts.writes++;
      req.resume();
      res.on("close", () => counts.writeClosed++);
      if (unknownWrite) return;
      res.writeHead(405).end();
      return;
    }
    if (path === "/") {
      res.writeHead(200, { "Content-Type": "text/html;charset=utf-8" })
        .end(`<!doctype html><meta charset="utf-8"><button id="leave">外部离开</button><div id="app"></div><script type="module">
import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};
import Batch from ${JSON.stringify(vite + component)};
window.expiredEvents=0;
const app=createApp({render:()=>h(Batch,{book:${JSON.stringify(book)},project:{id:'project',name:'隔离作品',role:'owner',revision:1},actor:{id:'owner',role:'owner',workspace_id:'w',username:'owner'},onExpired:()=>window.expiredEvents++})});
app.mount('#app');document.querySelector('#leave').onclick=()=>app.unmount();</script>`);
      return;
    }
    if (
      path.endsWith("/writing-batches/chapters") ||
      path.endsWith("/generation-options")
    ) {
      counts.configReads++;
      if (failedPair === "config") {
        if (path.endsWith("/chapters"))
          res
            .writeHead(503, { "Content-Type": "application/json" })
            .end('{"error":"first read failed"}');
        else {
          res.writeHead(403, { "Content-Type": "application/json" });
          res.flushHeaders();
          res.write('{"error":');
          hang();
        }
        return;
      }
      if (mode === "initial" && counts.configReads <= 2) {
        hang();
        return;
      }
      if (path.endsWith("/chapters"))
        json({
          items: [
            { id: "c1", title: "第一章", ready: true, reason: "" },
            { id: "c2", title: "第二章", ready: true, reason: "" },
          ],
        });
      else
        json({
          items: [
            {
              role: "writer",
              name: "隔离模型",
              model: "fixture-only",
              price: { id: "p1", currency: "CNY" },
            },
          ],
        });
    } else if (path === root) {
      if (denyList) {
        res.writeHead(403, { "Content-Type": "application/json" });
        res.flushHeaders();
        res.write('{"error":');
        hang();
        return;
      }
      json({ items: [run, other] });
    } else if (path === `${root}/${run.id}`) {
      if (holdDetail) {
        holdDetail = false;
        hang();
        releaseDenied = () => {
          if (!res.destroyed)
            res
              .writeHead(403, { "Content-Type": "application/json" })
              .end('{"error":"旧批次权限拒绝"}');
        };
        return;
      }
      json(run);
    } else if (path === `${root}/${other.id}`) json(other);
    else if (path.endsWith("/res1/draft") || path.endsWith("/res1/writer/g1")) {
      if (failedPair === "preview") {
        if (path.endsWith("/draft"))
          res
            .writeHead(503, { "Content-Type": "application/json" })
            .end('{"error":"first read failed"}');
        else {
          res.writeHead(403, { "Content-Type": "application/json" });
          res.flushHeaders();
          res.write('{"error":');
          hang();
        }
        return;
      }
      if (holdPreview) {
        hang();
        return;
      }
      json({
        resource: { id: "res1" },
        revision: 1,
        base_head: "",
        document: {
          title: "隔离章稿",
          text: path.endsWith("/draft") ? "原私人稿" : "新候选正文",
        },
        notes: [],
      });
    } else res.writeHead(404).end();
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const now = new Date("2026-10-29T00:00:00Z");
  await page.clock.install({ time: now });
  await page.clock.pauseAt(now);
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  return {
    counts,
    holdDetail: () => {
      holdDetail = true;
    },
    releaseDenied: () => releaseDenied?.(),
    unknownWrite: () => {
      unknownWrite = true;
    },
    denyList: () => {
      denyList = true;
    },
    allowPreview: () => {
      holdPreview = false;
    },
    needReconciliation: () => {
      run.state = "BLOCKED";
      run.uncertain_calls = 1;
    },
    failPair: (pair?: "config" | "preview") => {
      failedPair = pair;
    },
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

test("连写首屏读取悬挂后可返回与原地重试，保留要求和数量且不写入", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "initial");
  try {
    await expect.poll(() => state.counts.hangs).toBe(2);
    await page.clock.fastForward(15000);
    await expect.poll(() => state.counts.aborted).toBe(2);
    await expect(
      page.getByRole("button", { name: "返回小说", exact: true }),
    ).toBeEnabled();
    await page.getByLabel("连续章节数").fill("1");
    await page.getByLabel("本批要求").fill("保留作者这次的要求");
    await page.getByLabel("本批总费用上限").fill("3");
    await page
      .getByRole("button", { name: "重试加载工作台", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "准备 1 章", exact: true }),
    ).toBeEnabled();
    await expect(page.getByLabel("连续章节数")).toHaveValue("1");
    await expect(page.getByLabel("本批要求")).toHaveValue("保留作者这次的要求");
    await expect(page.getByLabel("本批总费用上限")).toHaveValue("3");
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
});

test("换批次后迟到权限错误不清新视图，当前拒绝即使body挂起也清快照", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    await page.getByRole("button", { name: /111111$/ }).click();
    await expect(
      page.getByRole("button", { name: "暂停任务", exact: true }),
    ).toBeEnabled();
    state.holdDetail();
    await page.clock.fastForward(2500);
    await expect.poll(() => state.counts.hangs).toBe(1);
    await page.getByRole("button", { name: /222222$/ }).click();
    const progress = page.getByRole("region", { name: "连续章节进度" });
    await expect(progress).toContainText("已记费用 0.5");
    state.releaseDenied();
    await page.clock.runFor(100);
    await expect(progress).toContainText("已记费用 0.5");
    await expect(page.getByRole("alert")).toHaveCount(0);
    await page.getByLabel("本批要求").fill("保留未保存要求");
    state.denyList();
    await page.getByRole("button", { name: "刷新记录", exact: true }).click();
    await expect.poll(() => state.counts.hangs).toBe(2);
    await page.clock.fastForward(15000);
    await expect(progress).toHaveCount(0);
    await expect(page.getByRole("button", { name: /111111$/ })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /222222$/ })).toHaveCount(0);
    await expect(page.getByLabel("起始章节").locator("option")).toHaveCount(0);
    await expect(page.getByLabel("本批要求")).toHaveValue("保留未保存要求");
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
});

test("离开连写页取消读取，未知控制写请求不被超时取消或重发", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    await page.getByRole("button", { name: /111111$/ }).click();
    await expect(
      page.getByRole("button", { name: "暂停任务", exact: true }),
    ).toBeEnabled();
    state.holdDetail();
    await page.clock.fastForward(2500);
    await expect.poll(() => state.counts.hangs).toBe(1);
    state.unknownWrite();
    await page.getByRole("button", { name: "暂停任务", exact: true }).click();
    await expect.poll(() => state.counts.writes).toBe(1);
    await page.getByRole("button", { name: "外部离开", exact: true }).click();
    await expect.poll(() => state.counts.aborted).toBe(1);
    await page.clock.fastForward(30000);
    expect(state.counts.writes).toBe(1);
    expect(state.counts.writeClosed).toBe(0);
    expect(
      await page.evaluate(
        () => (window as unknown as { expiredEvents: number }).expiredEvents,
      ),
    ).toBe(0);
  } finally {
    await state.close();
  }
});

test("候选对照读取超时可重试，未确认时不会采用或覆盖私人稿", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "preview");
  try {
    await page.getByRole("button", { name: /111111$/ }).click();
    await page.getByRole("button", { name: "对照与采用", exact: true }).click();
    await expect.poll(() => state.counts.hangs).toBe(2);
    await page.clock.fastForward(15000);
    await expect.poll(() => state.counts.aborted).toBe(2);
    await expect(
      page.getByRole("button", { name: "返回小说", exact: true }),
    ).toBeEnabled();
    state.allowPreview();
    await page.getByRole("button", { name: "对照与采用", exact: true }).click();
    const compare = page.getByRole("region", { name: "批次章节候选对照" });
    await expect(compare).toContainText("原私人稿");
    await expect(compare).toContainText("新候选正文");
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
});

test("恢复工作台可查找筛选并打开最近批次，未知费用只引导核对而不执行任务", async ({
  page,
}, info) => {
  const state = await fixture(page, info);
  try {
    await expect(page.getByRole("button", { name: /222222$/ })).toBeEnabled();
    await page.getByLabel("查找批次", { exact: true }).fill("第二章");
    await expect(page.getByRole("button", { name: /111111$/ })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /222222$/ })).toBeVisible();
    await page.getByLabel("查找批次", { exact: true }).fill("");
    await page
      .getByRole("button", { name: "打开最近未完成批次", exact: true })
      .click();
    const progress = page.getByRole("region", { name: "连续章节进度" });
    await expect(progress).toContainText("已记费用 0.2");
    expect(state.counts.writes).toBe(0);
    state.needReconciliation();
    await page.getByRole("button", { name: "刷新记录", exact: true }).click();
    await expect(progress).toContainText("先核对未知费用");
    await page
      .getByLabel("批次状态", { exact: true })
      .selectOption("attention");
    await expect(page.getByRole("button", { name: /111111$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /222222$/ })).toHaveCount(0);
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
});

test("并行读取先普通失败时，稍后的权限拒绝仍清除工作台及候选快照", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "preview");
  try {
    state.allowPreview();
    await page.getByRole("button", { name: /111111$/ }).click();
    await expect(
      page.getByRole("region", { name: "连续章节进度" }),
    ).toBeVisible();
    state.failPair("config");
    await page
      .getByRole("button", { name: "刷新章节与模型", exact: true })
      .click();
    await expect.poll(() => state.counts.hangs).toBe(1);
    await page.clock.fastForward(15000);
    await expect(
      page.getByRole("region", { name: "连续章节进度" }),
    ).toHaveCount(0);
    state.failPair();
    await page
      .getByRole("button", { name: "重试加载工作台", exact: true })
      .click();
    await page.getByRole("button", { name: /111111$/ }).click();
    await page.getByRole("button", { name: "对照与采用", exact: true }).click();
    await expect(
      page.getByRole("region", { name: "批次章节候选对照" }),
    ).toContainText("新候选正文");
    state.failPair("preview");
    await page.getByRole("button", { name: "对照与采用", exact: true }).click();
    await expect.poll(() => state.counts.hangs).toBe(2);
    await page.clock.fastForward(15000);
    await expect(
      page.getByRole("region", { name: "连续章节进度" }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("region", { name: "批次章节候选对照" }),
    ).toHaveCount(0);
    expect(state.counts.writes).toBe(0);
  } finally {
    await state.close();
  }
});
