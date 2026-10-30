import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";

async function fixture(page: Page, info: TestInfo, planning = false) {
  const component = planning
    ? "/src/features/novel/NovelPlanning.vue"
    : "/src/features/novel/NovelManuscriptDesk.vue";
  const compiled = await (await page.request.get(component)).text();
  const vue = compiled.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  const text =
    "第一章 雨夜\r\n旧城🌙来信。\r\n第二章 重逢\n原文保留。\n第三章 尾声\n后续。";
  const chars = Array.from(text),
    a = chars.join("").indexOf("第二章");
  const start2 = Array.from(text.slice(0, a)).length;
  const start3 = Array.from(text.slice(0, text.indexOf("第三章"))).length;
  const source = {
    id: "source",
    name: "长稿.txt",
    hash: "source-hash",
    size: 300000,
    characters: chars.length,
    complete: true,
    extracted: 1,
    pending: 0,
    notes: [],
  };
  let batch = {
    id: "batch",
    revision: 1,
    hash: "layout-1",
    document: {
      recovered: false,
      schema: "novel-manuscript-import-v1",
      book_id: "book",
      source_id: "source",
      source_hash: "source-hash",
      text_hash: "text-hash",
      total: chars.length,
      complete: true,
      accept_partial: false,
      created_at: "2026-10-30T02:00:00Z",
      operation_id: "create",
      parts: [
        {
          id: "p1",
          start: 0,
          end: start2,
          title: "第一章 雨夜",
          volume: "第一卷",
          mode: "new",
        },
        {
          id: "p2",
          start: start2,
          end: start3,
          title: "第二章 重逢",
          volume: "第一卷",
          mode: "new",
        },
        {
          id: "p3",
          start: start3,
          end: chars.length,
          title: "第三章 尾声",
          volume: "第二卷",
          mode: "new",
        },
      ],
    },
    progress: [] as Record<string, unknown>[],
  };
  let mode: "normal" | "fail-second" | "hold" | "deny" = "normal",
    duplicate = false;
  const held: (() => void)[] = [];
  const applies: string[] = [],
    writes: string[] = [];
  let arrivals = 0;
  const plan = {
    resource: {
      id: "volume-control",
      name: "卷安排",
      kind: "novel_plan_volume",
      head: "",
      revision: 1,
    },
    revision: 1,
    hash: "plan-hash",
    base_head: "",
    ready: false,
    reason: "待完善",
    document: {
      schema: 1,
      kind: "volume",
      scope: "volume",
      title: "第一卷",
      settings_revision: 1,
      settings_hash: "settings",
      ip_head: "head",
      inputs: [],
      chapters: [] as Record<string, unknown>[],
    },
    published: null,
  };
  const server = createServer((req, res) => {
    const url = new URL(req.url!, "http://127.0.0.1");
    const json = (v: unknown, status = 200) =>
      res
        .writeHead(status, { "Content-Type": "application/json" })
        .end(JSON.stringify(v));
    if (url.pathname === "/")
      return res
        .writeHead(200, { "Content-Type": "text/html; charset=utf-8" })
        .end(
          `<!doctype html><meta charset="utf-8"><div id="app"></div><pre id="edit"></pre><button id="leave">leave</button><script type="module">import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};import Component from ${JSON.stringify(vite + component)};import ${JSON.stringify(vite + "/src/style.css")};window.expired=0;const app=createApp({render:()=>h(Component,{book:{id:'book',project_id:'project',branch_id:'branch',setup:{title:'雾城'}},initialBatchId:'batch',initialResourceId:'volume-control',canEdit:true,project:{id:'project',role:'owner'},actor:{id:'owner',role:'owner'},onEdit:id=>document.querySelector('#edit').textContent=id,onExpired:()=>window.expired++})});app.mount('#app');document.querySelector('#leave').onclick=()=>app.unmount();</script>`,
        );
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      const input =
        req.headers["content-type"]?.includes("application/json") && body
          ? JSON.parse(body)
          : {};
      if (req.method !== "GET") writes.push(url.pathname);
      if (mode === "deny") return json({ error: "访问权限已撤回" }, 403);
      if (url.pathname.endsWith("/plans"))
        return json({
          stage: "OUTLINE_PLANNING",
          resources: [plan.resource],
          sources: [],
          reason: "待完善",
        });
      if (url.pathname.endsWith("/plans/volume-control/draft")) {
        if (req.method === "PUT") {
          plan.document = input.document;
          plan.revision++;
        }
        return json(plan);
      }
      if (url.pathname.includes("/chapter-references/"))
        return json({
          book_id: "book",
          chapter_id: url.pathname.split("/").at(-1),
          control_id: "imported-control",
          title: "已导入的旧城",
          source: "working",
          origin: "private",
          text: "原稿保留。",
        });
      if (url.pathname.endsWith("/sources"))
        return json(req.method === "POST" ? source : { items: [source] });
      if (url.pathname.endsWith("/sources/receipt")) return json({ source });
      if (url.pathname.endsWith("/by-source/source")) return json({ batch });
      if (url.pathname.endsWith("/manuscripts"))
        return json(req.method === "GET" ? { items: [batch] } : batch);
      if (url.pathname.endsWith("/batch") && req.method === "PUT") {
        batch = {
          ...batch,
          revision: batch.revision + 1,
          hash: `layout-${batch.revision + 1}`,
          document: {
            ...batch.document,
            parts: input.parts,
            accept_partial: input.accept_partial,
            operation_id: input.operation_id,
          },
        };
        return json(batch);
      }
      if (url.pathname.endsWith("/batch")) return json(batch);
      const partID = url.pathname.split("/parts/")[1]?.split("/")[0];
      const p = batch.document.parts.find((p) => p.id === partID);
      if (p && url.pathname.endsWith("/apply")) {
        applies.push(p.id);
        const old = batch.progress.find((v) => v.part_id === p.id);
        const progress = {
          batch_id: "batch",
          part_id: p.id,
          status:
            mode === "fail-second" && p.id === "p2"
              ? "failed"
              : p.mode === "skip"
                ? "skipped"
                : "done",
          reason:
            mode === "fail-second" && p.id === "p2"
              ? "该章导入中断，已保留原稿和已完成进度。"
              : "",
          chapter_id: `chapter-${p.id}`,
          control_id: `control-${p.id}`,
          saved_revision: 1,
          saved_hash: `hash-${p.id}`,
          operation_id: input.operation_id,
          revision: Number(old?.revision ?? 0) + 1,
        };
        batch.progress = [
          ...batch.progress.filter((v) => v.part_id !== p.id),
          progress,
        ];
        arrivals++;
        if (mode === "hold") {
          held.push(() => json(progress));
          return;
        }
        return json(progress);
      }
      if (p) {
        const start = p.start + Number(url.searchParams.get("offset") || 0),
          end = Math.min(p.end, start + 20000);
        return json({
          part_id: p.id,
          text: chars.slice(start, end).join(""),
          start,
          end,
          next: end < p.end ? end - p.start : 0,
          characters: p.end - p.start,
          bytes: (p.end - p.start) * 3,
          importable: true,
          matches: duplicate
            ? [
                {
                  control_id: "existing",
                  chapter_id: "existing-chapter",
                  title: p.title,
                  exact: true,
                  revision: 7,
                  hash: "target-7",
                  base_head: "head",
                },
              ]
            : [],
          uncertain: 0,
          notes: [],
        });
      }
      return json({ items: [] });
    });
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  await page.clock.install({ time: new Date("2026-10-30T03:00:00Z") });
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  await expect(
    page.getByRole("button", {
      name: planning ? "将已有章稿加入本卷草稿" : "读取正文与重复核对",
    }),
  ).toBeVisible();
  return {
    applies,
    writes,
    plan,
    recover() {
      batch.document.recovered = true;
    },
    get batch() {
      return batch;
    },
    get arrivals() {
      return arrivals;
    },
    setMode(v: typeof mode) {
      mode = v;
    },
    duplicate() {
      duplicate = true;
    },
    release() {
      held.splice(0).forEach((f) => f());
    },
    async close() {
      server.closeAllConnections();
      await new Promise<void>((r) => server.close(() => r()));
    },
  };
}
test("长稿分章可调整、合并、换序，保存后从失败处续导且不重复已完成章节", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    await page.getByRole("button", { name: "读取正文与重复核对" }).click();
    await expect(
      page.getByRole("textbox", { name: "分章原文预览" }),
    ).toHaveValue(/旧城🌙/);
    await page.getByRole("spinbutton", { name: "分章位置" }).fill("8");
    await page.getByRole("button", { name: "在此分为两章" }).click();
    await expect(page.getByText(/分章调整尚未保存/)).toBeVisible();
    await expect(
      page.getByRole("button", { name: "从未完成处继续导入" }),
    ).toBeDisabled();
    await page.getByRole("button", { name: "合并后续原文" }).click();
    await page
      .getByRole("textbox", { name: "章节标题", exact: true })
      .fill("第一章 修订标题");
    await page.getByRole("button", { name: "保存分章方案" }).click();
    await expect(page.getByText(/分章调整尚未保存/)).toHaveCount(0);
    f.setMode("fail-second");
    await page.getByRole("button", { name: "从未完成处继续导入" }).click();
    await expect(
      page
        .getByText("该章导入中断，已保留原稿和已完成进度。", { exact: true })
        .first(),
    ).toBeVisible();
    expect(f.applies).toEqual(["p1", "p2"]);
    f.setMode("normal");
    await page.getByRole("button", { name: "从未完成处继续导入" }).click();
    await expect(page.getByText(/3段 · 已完成3段/)).toBeVisible();
    expect(f.applies).toEqual(["p1", "p2", "p2", "p3"]);
    await page.getByRole("button", { name: "打开已导入章节" }).click();
    await expect(page.locator("#edit")).toHaveText("chapter-p3");
    await page.reload();
    await expect(page.getByText(/3段 · 已完成3段/)).toBeVisible();
    await expect(
      page.getByRole("button", { name: "从未完成处继续导入" }),
    ).toBeDisabled();
    await page.screenshot({
      path: info.outputPath("manuscript-desktop.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: info.outputPath("manuscript-mobile.png"),
      fullPage: true,
    });
  } finally {
    await f.close();
  }
});
test("恢复副本的旧分章只读，但仍可上传原稿建立新导入", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    f.recover();
    await page.reload();
    await expect(page.getByText(/这是项目恢复副本/)).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "章节标题", exact: true }),
    ).toBeDisabled();
    await expect(
      page.getByRole("button", { name: "从未完成处继续导入" }),
    ).toBeDisabled();
    await page.getByLabel("选择长稿文件").setInputFiles({
      name: "恢复后的原稿.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("第一章 新记录\n原稿保留。"),
    });
    await expect(
      page.getByRole("button", { name: "上传并保留原稿" }),
    ).toBeEnabled();
    await page.getByRole("button", { name: "上传并保留原稿" }).click();
    await expect
      .poll(() => f.writes.filter((p) => p.endsWith("/sources")).length)
      .toBe(1);
    await expect(
      page.getByRole("button", { name: "分章预览 / 继续该原稿" }),
    ).toBeEnabled();
  } finally {
    await f.close();
  }
});
test("重复正文需要先选择目标、保存版本并明确替换", async ({ page }, info) => {
  const f = await fixture(page, info);
  try {
    f.duplicate();
    await page.getByRole("button", { name: "读取正文与重复核对" }).click();
    await expect(page.getByText(/相同正文：第一章/)).toBeVisible();
    await page.getByRole("button", { name: "选择替换此稿" }).click();
    await expect(
      page.getByRole("button", { name: "导入 / 重试本段" }),
    ).toBeDisabled();
    await page.getByRole("button", { name: "保存分章方案" }).click();
    expect(f.batch.document.parts[0]).toMatchObject({
      mode: "replace",
      target: { control_id: "existing", revision: 7, hash: "target-7" },
    });
    await page.getByRole("checkbox", { name: /明确替换分章/ }).check();
    await page.getByRole("button", { name: "导入 / 重试本段" }).click();
    await expect(
      page.getByRole("button", { name: "打开已导入章节" }),
    ).toBeVisible();
  } finally {
    await f.close();
  }
});
test("导入响应超时只核对已保存进度，迟到响应不会再导入", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    f.setMode("hold");
    await page.getByRole("button", { name: "导入 / 重试本段" }).click();
    await expect.poll(() => f.arrivals).toBe(1);
    await page.clock.fastForward(15010);
    await expect(page.getByText(/本次结果仍待核对/)).toBeVisible();
    await page.getByRole("button", { name: "核对结果", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "打开已导入章节" }),
    ).toBeVisible();
    f.release();
    await expect(
      page.getByRole("button", { name: "导入 / 重试本段" }),
    ).toBeDisabled();
    expect(f.applies).toEqual(["p1"]);
  } finally {
    await f.close();
  }
});
test("核对发现权限撤回后，迟到成功不能重新显示原稿或进度", async ({
  page,
}, info) => {
  const f = await fixture(page, info);
  try {
    f.setMode("hold");
    await page.getByRole("button", { name: "导入 / 重试本段" }).click();
    await expect.poll(() => f.arrivals).toBe(1);
    await page.clock.fastForward(15010);
    f.setMode("deny");
    await page.getByRole("button", { name: "核对结果", exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText("访问权限已撤回");
    f.release();
    await expect(
      page.getByRole("textbox", { name: "章节标题", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "打开已导入章节" }),
    ).toHaveCount(0);
    expect(f.applies).toEqual(["p1"]);
  } finally {
    await f.close();
  }
});
test("已有导入章稿按稳定编号加入分卷草稿，保留正文且重复绑定被拒绝", async ({
  page,
}, info) => {
  const f = await fixture(page, info, true);
  try {
    const id = "1".repeat(32);
    await page.getByRole("textbox", { name: /已有章稿编号/ }).fill(id);
    await page.getByRole("button", { name: "将已有章稿加入本卷草稿" }).click();
    await expect(page.getByRole("status")).toContainText(
      "已有章稿已加入本卷规划草稿",
    );
    await page.clock.fastForward(650);
    await expect.poll(() => f.plan.document.chapters.length).toBe(1);
    expect(f.plan.document.chapters[0]).toMatchObject({
      id,
      title: "已导入的旧城",
    });
    expect(f.writes).toEqual(["/api/novels/book/plans/volume-control/draft"]);
    await page.getByRole("textbox", { name: /已有章稿编号/ }).fill(id);
    await page.getByRole("button", { name: "将已有章稿加入本卷草稿" }).click();
    await expect(page.getByRole("alert")).toHaveText(
      "本卷已经包含该章，不会重复添加。",
    );
  } finally {
    await f.close();
  }
});
