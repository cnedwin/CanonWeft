import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";

const quote = "旧约未还。";
async function fixture(
  page: Page,
  info: TestInfo,
  kind: "workspace" | "issues" | "editor",
  options: { duplicate?: boolean; large?: boolean } = {},
) {
  const path = `/src/features/novel/${kind === "workspace" ? "NovelWorkspace" : kind === "issues" ? "NovelReviewIssues" : "NovelChapterEditor"}.vue`;
  const compiled = await (await page.request.get(path)).text();
  const vue = compiled.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  let text = options.duplicate
    ? `🌙甲段。${quote}间隔。${quote}尾声。`
    : `🌙开头。${quote}落幕。`;
  let revision = 1,
    hash = "reviewed-a",
    revisionB = 1,
    recordsRevision = 0;
  let failure: 0 | 403 | 503 = 0,
    writeMode: "normal" | "conflict" | "hold" | "wrong" = "normal";
  let entries: Record<string, unknown>[] = [];
  let pending: { input: Record<string, unknown>; done: () => void } | undefined;
  const counts = {
    reportReads: 0,
    recordReads: 0,
    writes: 0,
    saves: 0,
    closedWrites: 0,
    unexpected: [] as string[],
  };
  const chapter = (id: string) => ({
    id,
    title: "重逢",
    volume_id: id === "chapter-a" ? "v1" : "v2",
    volume: id === "chapter-a" ? "第一卷" : "第二卷",
    included: true,
    warning: "",
    hash: id === "chapter-a" ? "reviewed-a" : "reviewed-b",
  });
  const finding = (
    id: string,
    title: string,
    severity = "major",
    chapterID = "chapter-a",
  ) => ({
    id,
    title,
    severity,
    category: "causality",
    explanation: "前文约定仍未交代，读者难以理解因果。",
    suggestion: "补足钥匙归还的行动和时间。",
    evidence: [{ chapter_id: chapterID, quote }],
  });
  const report = {
    run: {
      id: "run-one",
      branch_id: "branch",
      state: "COMPLETED",
      revision: 1,
      paused: false,
      cancel_requested: false,
      restored_disabled: false,
      reason: "",
      currency: "CNY",
      budget_limit_micros: 1000000,
      spent_micros: 1000,
      held_micros: 0,
      uncertain_calls: 0,
      steps: [],
      frozen: {
        trial: false,
        definition: {},
        input: {
          kind: "novel-book-review-v1",
          book_id: "book",
          request: { source: "draft", volume_id: "", notes: "" },
        },
      },
    },
    preview: {
      hash: "frozen",
      included: 2,
      missing: 0,
      warnings: 0,
      passes: 2,
      chapters: [chapter("chapter-a"), chapter("chapter-b")],
    },
    stale: false,
    stale_reason: "",
    covered: 2,
    limits: [],
    passes: [
      {
        key: "pass-1",
        step_id: "step-one",
        output_hash: "output-one",
        chapter_ids: ["chapter-a"],
        result: {
          coverage: ["chapter-a"],
          checks: { causality: "issues_found" },
          findings: [
            finding("same-id", "钥匙流转缺口"),
            ...(options.large
              ? Array.from({ length: 55 }, (_, i) =>
                  finding(`extra-${i}`, `补充问题${i}`, "question"),
                )
              : []),
          ],
          carry: [],
          limits: [],
        },
      },
      {
        key: "pass-2",
        step_id: "step-two",
        output_hash: "output-two",
        chapter_ids: ["chapter-b"],
        result: {
          coverage: ["chapter-b"],
          checks: { causality: "issues_found" },
          findings: [
            finding("same-id", "另一段相同编号的问题", "minor", "chapter-b"),
          ],
          carry: [],
          limits: [],
        },
      },
    ],
  };
  const pins = () => [
    {
      chapter_id: "chapter-a",
      resource_id: "body-a",
      revision,
      hash,
      base_head: "",
      current_head: "",
      available: true,
      reason: "",
    },
    {
      chapter_id: "chapter-b",
      resource_id: "body-b",
      revision: revisionB,
      hash: `reviewed-b${revisionB === 1 ? "" : `-${revisionB}`}`,
      base_head: "",
      current_head: "",
      available: true,
      reason: "",
    },
  ];
  const records = () => ({
    resource_id: recordsRevision ? "notes" : "",
    revision: recordsRevision,
    hash: recordsRevision ? `notes-${recordsRevision}` : "",
    current_chapters: pins(),
    entries: entries.map((entry) => {
      const saved = entry.chapters as { chapter_id: string; hash: string }[];
      const stale = saved.some(
        (p) =>
          p.hash !== pins().find((c) => c.chapter_id === p.chapter_id)?.hash,
      );
      return {
        ...entry,
        stale,
        reason: stale ? "证据章稿已变化，请重新人工核对。" : "",
      };
    }),
  });
  function apply(input: Record<string, unknown>) {
    entries = entries.filter(
      (entry) =>
        !(
          entry.source_step_id === input.source_step_id &&
          entry.source_output_hash === input.source_output_hash &&
          entry.finding_id === input.finding_id
        ),
    );
    entries.push({
      source_step_id: input.source_step_id,
      source_output_hash: input.source_output_hash,
      pass_key: input.source_step_id === "step-one" ? "pass-1" : "pass-2",
      finding_id: input.finding_id,
      status: input.status,
      note: input.note,
      operation_id: input.operation_id,
      chapters: input.expected_chapters,
    });
    recordsRevision++;
  }
  const draft = () => ({
    resource: {
      id: "body-a",
      kind: "novel_chapter_control",
      name: "重逢",
      head: "",
      revision: 1,
    },
    revision,
    hash,
    base_head: "",
    ready: true,
    reason: "",
    document: {
      schema: 1,
      chapter_id: "chapter-a",
      body_id: "text-a",
      detail: {
        kind: "resource",
        id: "detail",
        branch_id: "branch",
        revision: "detail-head",
        hash: "detail-hash",
      },
      settings_revision: 1,
      settings_hash: "settings",
      ip_head: "ip-head",
      title: "重逢",
      text,
      story_time: 1,
      perspective: "",
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
        .end(`<!doctype html><meta charset="utf-8"><button id="leave">离开测试页</button><div id="app"></div><script type="module">
import {createApp,h} from ${JSON.stringify(new URL(vue!, vite).href)};
import Component from ${JSON.stringify(vite + path)};
const book={id:'book',project_id:'project',branch_id:'branch',settings_revision:1,stage:'WORLD_PLANNING',setup:{title:'雾城',genre:'悬疑',language:'简体中文',idea:'调查旧案',conflict:'真相与承诺',target_words:300000,volume_count:2,chapter_min:2000,chapter_max:4000,characters:[]}};
const report=${JSON.stringify(report)};
const target={run_id:'run-one',step_id:'step-one',output_hash:'output-one',finding_id:'same-id',chapter_id:'chapter-a',quote:${JSON.stringify(quote)},reviewed_hash:'reviewed-a',title:'钥匙流转缺口',explanation:'因果待核对',suggestion:'补足归还过程'};
window.expiredEvents=0;const app=createApp({render:()=>h(Component,{book,project:{id:'project',name:'雾城',role:'owner',revision:1},actor:{id:'owner',workspace_id:'workspace',username:'owner',role:'owner'},chapterId:'chapter-a',report,root:'/novels/book/book-reviews',disabled:false,reviewTarget:target,onExpired:()=>window.expiredEvents++})});app.mount('#app');document.querySelector('#leave').onclick=()=>app.unmount();</script>`);
    let body = "";
    request.on("data", (chunk) => {
      body += chunk;
    });
    request.on("end", () => {
      const input = body ? JSON.parse(body) : {};
      if (url.pathname === "/api/novels/book/plans")
        return json({
          stage: "WORLD_PLANNING",
          reason: "可手动查看",
          resources: [],
          sources: [],
        });
      if (url.pathname.endsWith("/generation-options"))
        return json({ error: "模型设置暂不可读" }, 503);
      if (url.pathname.endsWith("/capabilities"))
        return json({ items: [], next_cursor: "" });
      if (url.pathname.endsWith("/export/preview"))
        return json({
          ...report.preview,
          title: "雾城",
          source: "draft",
          characters: 20,
        });
      if (url.pathname === "/api/novels/book/book-reviews")
        return json({ items: [report.run] });
      if (url.pathname === "/api/novels/book/book-reviews/run-one") {
        counts.reportReads++;
        return json({
          ...report,
          stale: revision > 1,
          stale_reason: revision > 1 ? "章稿已改变" : "",
        });
      }
      if (url.pathname.endsWith("/dispositions")) {
        if (request.method === "GET") {
          counts.recordReads++;
          return failure
            ? json(
                {
                  error:
                    failure === 403
                      ? "当前账号无权读取本报告"
                      : "处理记录暂不可读",
                },
                failure,
              )
            : json(records());
        }
        if (request.method === "PUT") {
          counts.writes++;
          if (writeMode === "conflict")
            return json({ error: "章稿或处理记录已变化，请重新读取" }, 409);
          if (writeMode === "hold") {
            pending = {
              input,
              done: () => json({ error: "提交结果待核对" }, 503),
            };
            response.on("close", () => counts.closedWrites++);
            return;
          }
          if (writeMode === "wrong") return json(records());
          apply(input);
          return json(records());
        }
      }
      if (
        url.pathname === "/api/novels/book/chapters" &&
        request.method === "POST"
      )
        return json(draft().resource);
      if (url.pathname === "/api/novels/book/chapters/body-a/draft") {
        if (request.method === "PUT") {
          counts.saves++;
          text = input.document.text;
          revision++;
          hash = `changed-${revision}`;
        }
        return json(draft());
      }
      if (url.pathname === "/api/projects/project/branches/branch")
        return json({
          id: "branch",
          head: "ip-head",
          snapshot: { entities: [] },
        });
      if (request.method !== "GET")
        counts.unexpected.push(`${request.method} ${url.pathname}`);
      json({ error: `Unexpected ${request.method} ${url.pathname}` }, 404);
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
    records,
    mode: (value: typeof writeMode) => {
      writeMode = value;
    },
    deny: (value: typeof failure) => {
      failure = value;
    },
    changeOtherChapter: () => {
      revisionB++;
    },
    changePrimaryChapter: () => {
      revision++;
      hash = `changed-${revision}`;
    },
    releaseUnknown: (committed: boolean) => {
      if (pending) {
        if (committed) apply(pending.input);
        pending.done();
        pending = undefined;
      }
    },
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

test("大报告按页筛选，同段内外同编号各自处理，人工核对绑定证据版本且章稿变化失效", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "issues", { large: true });
  try {
    const desk = page.getByRole("region", { name: "审稿问题工作台" });
    await expect(desk).toContainText("共 57 项问题");
    await expect(desk.locator("[data-review-issue]")).toHaveCount(50);
    await page.getByLabel("问题严重程度").selectOption("major");
    await expect(desk.locator("[data-review-issue]")).toHaveCount(1);
    await desk.getByRole("button", { name: "定位并处理此问题" }).click();
    await page.getByLabel("当前问题人工处理状态").selectOption("confirmed");
    await page
      .getByLabel("问题处理备注")
      .fill("已检查本段全部证据，归还过程合理。");
    await expect(
      page.getByRole("button", { name: "保存人工处理记录", exact: true }),
    ).toBeDisabled();
    await page.getByRole("checkbox").check();
    await page
      .getByRole("button", { name: "保存人工处理记录", exact: true })
      .click();
    await expect(desk).toContainText("有效人工核对 1 项");
    expect(state.records().entries).toHaveLength(1);
    await page.getByLabel("问题处理备注").fill("再次核对前先保留我的新备注。");
    await page.getByRole("checkbox").check();
    state.mode("conflict");
    await page
      .getByRole("button", { name: "保存人工处理记录", exact: true })
      .click();
    await expect(desk.getByRole("alert")).toContainText("已变化");
    state.changePrimaryChapter();
    await page.getByRole("button", { name: "重新读取处理记录" }).click();
    await expect(page.getByRole("checkbox")).not.toBeChecked();
    await expect(page.getByLabel("问题处理备注")).toHaveValue(
      "再次核对前先保留我的新备注。",
    );
    await expect(
      page.getByRole("button", { name: "保存人工处理记录", exact: true }),
    ).toBeDisabled();
    state.mode("normal");
    await page.getByRole("checkbox").check();
    await page
      .getByRole("button", { name: "保存人工处理记录", exact: true })
      .click();
    await expect(desk).toContainText("有效人工核对 1 项");
    await page.getByLabel("问题严重程度").selectOption("minor");
    await desk.getByRole("button", { name: "定位并处理此问题" }).click();
    await page.getByLabel("当前问题人工处理状态").selectOption("deferred");
    await page.getByLabel("问题处理备注").fill("另一段稍后处理。");
    await page
      .getByRole("button", { name: "保存人工处理记录", exact: true })
      .click();
    expect(state.records().entries).toHaveLength(2);
    expect(
      new Set(state.records().entries.map((entry) => entry.source_step_id))
        .size,
    ).toBe(2);
    state.changePrimaryChapter();
    await page.getByRole("button", { name: "重新读取处理记录" }).click();
    await expect(desk).toContainText("历史处理需重核 1 项");
    await expect(desk).toContainText("有效人工核对 0 项");
    expect(state.counts.unexpected).toEqual([]);
  } finally {
    await state.close();
  }
});

test("报告在模型设置读取失败时仍可用，引用定位不改文，保存后回到原报告和筛选", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "workspace");
  try {
    await page
      .getByRole("navigation", { name: "本书工作区" })
      .getByRole("button", { name: "审稿与导出", exact: true })
      .click();
    await page
      .getByRole("button", { name: "整卷与全书审稿", exact: true })
      .click();
    await page.getByRole("button", { name: /已完成.*run-one/ }).click();
    const issues = page.getByRole("region", { name: "审稿问题工作台" });
    await expect(issues).toBeVisible();
    await page.getByLabel("搜索审稿问题").fill("钥匙流转");
    await issues.getByRole("button", { name: "定位并处理此问题" }).click();
    await page
      .getByRole("button", { name: "打开《重逢》定位编辑", exact: true })
      .click();
    const text = page.getByLabel("章节正文", { exact: true });
    await expect(text).toHaveValue(`🌙开头。${quote}落幕。`);
    await page.getByRole("button", { name: "定位原引用", exact: true }).click();
    expect(
      await text.evaluate((field: HTMLTextAreaElement) =>
        field.value.slice(field.selectionStart, field.selectionEnd),
      ),
    ).toBe(quote);
    expect(state.counts.saves).toBe(0);
    await page
      .getByLabel("这次希望怎样修改", { exact: true })
      .fill("保留原有语气。");
    await page.getByRole("button", { name: "带入原地精修要求" }).click();
    await expect(
      page.getByLabel("这次希望怎样修改", { exact: true }),
    ).toHaveValue(/保留原有语气。.*\n\n处理整卷/s);
    await text.fill("🌙作者补上归还钥匙的过程。落幕。");
    await page
      .getByRole("button", { name: "保存并返回审稿报告", exact: true })
      .click();
    await expect(issues).toBeVisible();
    await expect(page.getByLabel("搜索审稿问题")).toHaveValue("钥匙流转");
    await expect(
      page.getByRole("region", { name: "当前问题处理" }),
    ).toContainText("钥匙流转缺口");
    expect(state.counts.saves).toBe(1);
    expect(state.counts.reportReads).toBeGreaterThan(1);
    expect(state.counts.unexpected).toEqual([]);
  } finally {
    await state.close();
  }
});

test("重复引用必须选择具体位置，文字再改后无匹配不伪造定位或视为解决", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "editor", { duplicate: true });
  try {
    const text = page.getByLabel("章节正文", { exact: true });
    await expect(text).toHaveValue(/🌙甲段/);
    const anchor = page.getByRole("region", { name: "本次审稿修改任务" });
    await expect(anchor).toContainText("2 处相同引用");
    await anchor.getByRole("button", { name: "定位第 2 处" }).click();
    const position = await text.evaluate((field: HTMLTextAreaElement) => ({
      start: field.selectionStart,
      value: field.value.slice(field.selectionStart, field.selectionEnd),
      last: field.value.lastIndexOf("旧约未还。"),
    }));
    expect(position.value).toBe(quote);
    expect(position.start).toBe(position.last);
    await text.fill("🌙作者已经重写此处，但尚需语义复核。");
    await expect(anchor).toContainText("引用消失不能作为问题已经解决的依据");
    await expect(anchor.getByRole("button", { name: /定位第/ })).toHaveCount(0);
    expect(state.counts.writes).toBe(0);
    expect(state.counts.unexpected).toEqual([]);
  } finally {
    await state.close();
  }
});

test("处置写请求不受读超时取消，未知结果按operation对账且不重发；409保留备注", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "issues");
  try {
    const issues = page.getByRole("region", { name: "审稿问题工作台" });
    await expect(issues).toContainText("共 2 项问题");
    await issues
      .locator("[data-review-issue]")
      .first()
      .getByRole("button", { name: "定位并处理此问题" })
      .click();
    await page.getByLabel("问题处理备注").fill("此次保存结果必须核实。");
    state.mode("hold");
    await page
      .getByRole("button", { name: "保存人工处理记录", exact: true })
      .click();
    await expect.poll(() => state.counts.writes).toBe(1);
    await page.clock.fastForward(16000);
    expect(state.counts.closedWrites).toBe(0);
    expect(state.counts.writes).toBe(1);
    state.releaseUnknown(true);
    await expect(issues.getByRole("alert")).toContainText("尚未确认");
    await expect(page.getByLabel("问题处理备注")).toHaveValue(
      "此次保存结果必须核实。",
    );
    await page.getByRole("button", { name: "重新读取处理记录" }).click();
    await expect(issues).toContainText("核实");
    expect(state.counts.writes).toBe(1);
    state.mode("conflict");
    await page.getByLabel("问题处理备注").fill("手写备注继续保留。");
    await page
      .getByRole("button", { name: "保存人工处理记录", exact: true })
      .click();
    await expect(issues.getByRole("alert")).toContainText("已变化");
    await expect(page.getByLabel("问题处理备注")).toHaveValue(
      "手写备注继续保留。",
    );
    expect(state.counts.writes).toBe(2);
    state.mode("wrong");
    await page.getByRole("button", { name: "重新读取处理记录" }).click();
    await page
      .getByRole("button", { name: "保存人工处理记录", exact: true })
      .click();
    await expect(issues.getByRole("alert")).toContainText("尚未确认");
    await expect(page.getByLabel("问题处理备注")).toHaveValue(
      "手写备注继续保留。",
    );
    await page.getByRole("button", { name: "重新读取处理记录" }).click();
    await expect(issues).toContainText("尚未核实");
    expect(state.counts.writes).toBe(3);
    expect(state.counts.unexpected).toEqual([]);
  } finally {
    await state.close();
  }
});

test("临时读取故障保留未保存处置备注，当前权限拒绝清除整份受保护报告", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "workspace");
  try {
    await page
      .getByRole("navigation", { name: "本书工作区" })
      .getByRole("button", { name: "审稿与导出", exact: true })
      .click();
    await page
      .getByRole("button", { name: "整卷与全书审稿", exact: true })
      .click();
    await page.getByRole("button", { name: /已完成.*run-one/ }).click();
    const report = page.getByRole("article", { name: "审稿报告" });
    const issues = page.getByRole("region", { name: "审稿问题工作台" });
    await expect(issues).toBeVisible();
    await issues
      .locator("[data-review-issue]")
      .first()
      .getByRole("button", { name: "定位并处理此问题" })
      .click();
    await page
      .getByLabel("问题处理备注")
      .fill("临时故障不能覆盖我写的核对依据。");
    state.deny(503);
    await page.getByRole("button", { name: "重新读取处理记录" }).click();
    await expect(issues.getByRole("alert")).toContainText("暂不可读");
    await expect(page.getByLabel("问题处理备注")).toHaveValue(
      "临时故障不能覆盖我写的核对依据。",
    );
    state.deny(403);
    await page.getByRole("button", { name: "重新读取处理记录" }).click();
    await expect(report).toHaveCount(0);
    expect(state.counts.writes).toBe(0);
    expect(state.counts.unexpected).toEqual([]);
  } finally {
    await state.close();
  }
});
