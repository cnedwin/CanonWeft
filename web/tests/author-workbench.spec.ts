import { expect, test, type Page } from "@playwright/test";
const initialText = "门外。林舟停了下来。门后有人。";
async function fixture(page: Page, editor = false) {
  let draft = {
    resource: { id: "body", name: "第一章", revision: 1, head: "" },
    revision: 1,
    base_head: "",
    hash: "hash-1",
    ready: true,
    reason: "",
    document: {
      schema: 1,
      chapter_id: "chapter",
      body_id: "body",
      title: "第一章",
      text: initialText,
      references: [],
      story_time: 1,
      perspective: "",
      detail: {},
      settings_revision: 1,
      settings_hash: "settings",
      ip_head: "",
    },
  };
  let candidateText = "门外。林舟缓缓停步。门后有人。";
  let release: (() => void) | undefined;
  let held: Promise<void> | undefined;
  const writes: string[] = [];
  const requests: { path: string; method: string }[] = [];
  const task = {
    id: "task-111",
    state: "SUCCEEDED",
    cancelled: false,
    paused: false,
    frozen: {
      role: "writer",
      config: { name: "本地替身", revision: 1 },
      price: { currency: "CNY" },
      estimate_micros: 0,
    },
    output: { text: "本地候选", input_tokens: 0, output_tokens: 0 },
  };
  await page.route("**/api/**", async (route) => {
    const req = route.request(),
      path = new URL(req.url()).pathname;
    requests.push({ path, method: req.method() });
    if (path === "/api/novels/book/plans")
      return route.fulfill({
        json: {
          stage: "DETAIL_PLANNING",
          reason: "各章仍需确认细纲",
          resources: [],
          sources: [],
        },
      });
    if (path === "/api/novels/book/writing-batches/chapters")
      return route.fulfill({
        json: {
          items: [
            { id: "chapter", title: "第一章", ready: true, reason: "" },
            {
              id: "unready",
              title: "未确认章",
              ready: false,
              reason: "请确认细纲",
            },
          ],
        },
      });
    if (path === "/api/novels/book/chapters")
      return route.fulfill({ json: draft.resource });
    if (path.endsWith("/writing-card"))
      return route.fulfill({
        json: {
          revision: draft.revision,
          hash: draft.hash,
          card: {
            title: "找到门后的秘密",
            goal: "辨认信纸的来源",
            mainline: "追查失踪师父",
            theme: "承诺的代价",
            reader_promise: "公平线索",
            perspective: "林舟限知",
            people: "林舟",
            scene_goals: ["发现湿信纸"],
            guardrails: ["林舟尚不知道寄信人"],
            recent_plan: [
              {
                id: "prior",
                title: "上章安排",
                goal: "走到门外",
                change: "起了疑心",
                transition: "未敲门",
              },
            ],
            sources: [{ id: "detail" }],
            notes: ["此卡来自已确认规划"],
          },
        },
      });
    if (path.endsWith("/draft")) {
      if (req.method() === "PUT") {
        const input = req.postDataJSON();
        writes.push(input.document.text);
        draft = {
          ...draft,
          document: input.document,
          revision: draft.revision + 1,
          hash: `hash-${draft.revision + 1}`,
        };
      }
      return route.fulfill({ json: draft });
    }
    if (path === "/api/novels/book/chapters/body/writer/task-111") {
      const result = {
        ...draft,
        document: { ...draft.document, text: candidateText },
      };
      if (held) await held;
      return route.fulfill({ json: result });
    }
    if (path.endsWith("/agents") || path.endsWith("/polish-agent"))
      return route.fulfill({ json: {} });
    if (path === "/api/projects/project/branches/branch")
      return route.fulfill({
        json: { id: "branch", head: "", snapshot: { entities: [] } },
      });
    if (path.endsWith("/generation-options"))
      return route.fulfill({
        json: {
          items: [
            {
              role: "writer",
              name: "本地替身",
              model: "test-only",
              price: { id: "price", currency: "CNY" },
            },
          ],
        },
      });
    if (path.endsWith("/generations"))
      return route.fulfill({ json: { items: [task], runtime_ready: false } });
    if (path.endsWith("/generations/task-111"))
      return route.fulfill({ json: task });
    if (path.includes("/capabilities"))
      return route.fulfill({ json: { items: [] } });
    return route.fulfill({
      status: 404,
      json: { error: `未预期接口 ${path}` },
    });
  });
  await page.goto(
    `/tests/fixtures/author-workbench.html${editor ? "?editor" : ""}`,
  );
  return {
    requests,
    writes,
    candidate: (text: string) => {
      candidateText = text;
    },
    hold: () => {
      held = new Promise<void>((resolve) => {
        release = resolve;
      });
      return () => {
        held = undefined;
        release?.();
      };
    },
  };
}
async function openRefinement(page: Page) {
  const text = page.getByLabel("章节正文", { exact: true });
  await expect(text).toHaveValue(initialText);
  await text.evaluate((element: HTMLTextAreaElement) => {
    element.focus();
    element.setSelectionRange(3, 10);
    element.dispatchEvent(new Event("select", { bubbles: true }));
  });
  await page.getByRole("button", { name: "润色选段", exact: true }).click();
  await page
    .getByRole("button", { name: "查看任务 task-1", exact: true })
    .click();
}
test("当前阶段来自有效规划；助手写章必须明确选择；所有旧工具保留", async ({
  page,
}, testInfo) => {
  const state = await fixture(page);
  await expect(
    page.getByRole("region", { name: "本书当前阶段" }),
  ).toContainText("章节细纲与正文");
  await page.getByRole("button", { name: "写一章", exact: true }).click();
  await expect(
    page.getByRole("region", { name: "选择要写作的章节" }),
  ).toBeVisible();
  expect(
    state.requests.filter(
      (r) => r.path.endsWith("/chapters") && r.method === "POST",
    ),
  ).toHaveLength(0);
  await expect(page.getByRole("button", { name: /未确认章/ })).toBeDisabled();
  await page.getByText("全部创作工具", { exact: true }).click();
  for (const name of [
    "整书与分卷导出",
    "整卷与全书审稿",
    "封面与场景图",
    "资料变更与待复核",
    "批量角色与一句话完善",
    "本书角色与关系",
  ])
    await expect(page.getByRole("button", { name, exact: true })).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("workspace-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("workspace-mobile.png"),
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "写作 · 第一章", exact: true })
    .click();
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    initialText,
  );
});
test("选区精修使用原预算任务；越界候选不能采用且不改正文", async ({ page }) => {
  const state = await fixture(page, true);
  state.candidate("门外。林舟缓缓停步。连门后也改写了。");
  await openRefinement(page);
  await expect(
    page.getByRole("button", { name: "提取与编辑本章账本", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "更新写作依据，保留正文", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByLabel("希望 AI 协助的内容", { exact: true }),
  ).toHaveValue(/选区之外逐字保持原样/);
  await page.getByRole("button", { name: "查看正文候选", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("选区之外");
  await expect(
    page.getByRole("button", { name: "将候选加入私人稿", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    initialText,
  );
  expect(state.writes).toEqual([]);
  expect(state.requests.some((r) => /\/start$/.test(r.path))).toBe(false);
});
test("关闭精修后迟到候选不能覆盖未保存手改", async ({ page }) => {
  const state = await fixture(page, true);
  await openRefinement(page);
  const release = state.hold();
  await page.getByRole("button", { name: "查看正文候选", exact: true }).click();
  await page.getByRole("button", { name: "返回手动写作", exact: true }).click();
  await page.getByLabel("章节正文", { exact: true }).fill("作者的新句子。");
  release();
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    "作者的新句子。",
  );
  await expect(page.getByRole("region", { name: "正文候选对照" })).toHaveCount(
    0,
  );
});

test("正常选区候选并排比较后才采用，范围外正文原样保留", async ({ page }) => {
  const state = await fixture(page, true);
  await openRefinement(page);
  await page.getByRole("button", { name: "查看正文候选", exact: true }).click();
  const comparison = page.getByRole("region", { name: "正文候选对照" });
  await expect(comparison).toContainText("林舟停了下来。");
  await expect(comparison).toContainText("林舟缓缓停步。");
  expect(state.writes).toEqual([]);
  await page
    .getByRole("button", { name: "将候选加入私人稿", exact: true })
    .click();
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    "门外。林舟缓缓停步。门后有人。",
  );
  expect(state.writes).toEqual(["门外。林舟缓缓停步。门后有人。"]);
  await expect(
    page.getByText("候选已加入私人稿，可继续修改；尚未作为正式章节发布。", {
      exact: true,
    }),
  ).toBeVisible();
});

test("候选展示后作者继续手改立即失效，助手请求输入仍保留", async ({ page }) => {
  await fixture(page, true);
  await openRefinement(page);
  await page.getByRole("button", { name: "查看正文候选", exact: true }).click();
  await expect(
    page.getByRole("region", { name: "正文候选对照" }),
  ).toBeVisible();
  await page
    .getByLabel("章节正文", { exact: true })
    .fill("门外。作者新写的选择。门后有人。");
  await expect(page.getByRole("region", { name: "正文候选对照" })).toHaveCount(
    0,
  );
  const notes = page.getByLabel("希望 AI 协助的内容", { exact: true });
  await notes.fill("不能丢失的精修要求");
  await page.getByRole("button", { name: "返回手动写作", exact: true }).click();
  await expect(notes).toHaveValue("不能丢失的精修要求");
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    "门外。作者新写的选择。门后有人。",
  );
  await page
    .getByRole("button", { name: "放弃助手输入并返回正文", exact: true })
    .click();
  await expect(notes).toHaveCount(0);
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    "门外。作者新写的选择。门后有人。",
  );
});

test("写作卡按需读取，清楚区分近期规划与已发生正文", async ({ page }) => {
  const state = await fixture(page, true);
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    initialText,
  );
  expect(
    state.requests.filter((r) => r.path.endsWith("/writing-card")),
  ).toHaveLength(0);
  await page.getByText("本章写作卡与承接", { exact: true }).click();
  await expect(page.getByText("辨认信纸的来源", { exact: true })).toBeVisible();
  await expect(
    page.getByText("以下是规划安排；已发生剧情以采用的正文与账本为准。", {
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByLabel("章节正文", { exact: true })
    .fill("尚未保存的作者修改。");
  await expect(page.getByText("辨认信纸的来源", { exact: true })).toHaveCount(
    0,
  );
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    "尚未保存的作者修改。",
  );
});

test("写作卡回执版本不符时不展示过期材料", async ({ page }) => {
  await fixture(page, true);
  await page.route("**/writing-card", (route) =>
    route.fulfill({
      json: {
        revision: 0,
        hash: "old-hash",
        card: { title: "不能显示的旧规划" },
      },
    }),
  );
  await page.getByText("本章写作卡与承接", { exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("写作依据已经变化");
  await expect(page.getByText("不能显示的旧规划", { exact: true })).toHaveCount(
    0,
  );
  await expect(page.getByLabel("章节正文", { exact: true })).toHaveValue(
    initialText,
  );
});

test("精修期间正文改后再撤回也不能重新激活旧候选", async ({ page }) => {
  await fixture(page, true);
  await openRefinement(page);
  const text = page.getByLabel("章节正文", { exact: true });
  await text.fill("作者中途改过正文。");
  await text.fill(initialText);
  await page.getByRole("button", { name: "查看正文候选", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("旧候选已失效");
  await expect(page.getByRole("region", { name: "正文候选对照" })).toHaveCount(
    0,
  );
  await expect(text).toHaveValue(initialText);
});
