import { expect, test, type Page } from "@playwright/test";
import type { NovelDraft, NovelSetup } from "../src/features/novel/types";

const defaults: NovelSetup = {
  title: "",
  genre: "",
  language: "简体中文",
  source_mode: "fresh",
  source_text: "",
  ip_mode: "new",
  project_id: "",
  branch_id: "",
  base_head: "",
  idea: "",
  conflict: "",
  audience: "大众读者",
  world: "",
  theme: "",
  include: "",
  avoid: "",
  perspective: "第三人称限知",
  style: "中文类型小说表达",
  target_words: 300000,
  volume_count: 3,
  chapter_min: 2000,
  chapter_max: 4000,
  character_mode: "",
  characters: [],
  creation_mode: "",
  model_choice: "",
  connection_id: "",
  protection: "core_identity_life_world",
};
const presets = [
  ["urban-career", "都市事业", "男频"],
  ["fantasy-growth", "玄幻成长", "男频"],
  ["mystery-rules", "规则悬疑", "男频"],
  ["historical-strategy", "历史谋略", "男频"],
  ["modern-romance", "现代言情", "女频"],
  ["era-career", "年代事业", "女频"],
  ["ancient-intrigue", "古代权谋", "女频"],
  ["heroine-fantasy", "女主奇幻", "女频"],
].map(([id, name, audience]) => ({
  id,
  name,
  audience,
  revision: "2026-10-27.1",
  description: "独立编写的类型方法",
  example_idea: "修钟师寻找失踪朋友",
  reader_promise: "每卷揭开一层真相",
  skill_names: ["novel-planning"],
}));
const setup = (): NovelSetup => ({
  ...defaults,
  title: "旧城钟声",
  genre: "都市悬疑",
  idea: "修钟师寻找失踪朋友",
  conflict: "守住秘密还是救出朋友",
  world: "旧城钟表工坊",
  theme: "信任与代价",
  include: "每卷揭开一层真相",
  character_mode: "manual",
  characters: [
    {
      name: "林舟",
      identity: "修钟师",
      goal: "寻找朋友",
      background: "旧城长大",
      personality: "谨慎",
    },
  ],
  creation_mode: "assisted",
  model_choice: "later",
});
async function fixture(
  page: Page,
  options: { initial?: NovelDraft; missingCatalog?: boolean } = {},
) {
  let saved = options.initial ? structuredClone(options.initial) : undefined;
  const storedDrafts = new Map<string, NovelDraft>();
  if (saved) storedDrafts.set(saved.id, saved);
  const confirms: unknown[] = [];
  const calls: string[] = [];
  let release: (() => void) | undefined;
  let hold: Promise<void> | undefined;
  let holdSave: Promise<void> | undefined;
  let releaseSave: (() => void) | undefined;
  let conflict = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url()).pathname;
    calls.push(`${request.method()} ${url}`);
    if (url === "/api/setup")
      return route.fulfill({ json: { required: false } });
    if (url === "/api/me")
      return route.fulfill({
        json: {
          id: "owner",
          workspace_id: "w",
          username: "owner",
          role: "owner",
        },
      });
    if (
      [
        "/api/projects",
        "/api/novels",
        "/api/members",
        "/api/novel-setups/model-choices",
      ].includes(url)
    )
      return route.fulfill({ json: { items: [] } });
    if (url === "/api/novel-setups/defaults")
      return route.fulfill({ json: defaults });
    if (url === "/api/novel-setups/presets")
      return route.fulfill(
        options.missingCatalog
          ? { status: 503, json: { error: "目录暂不可用" } }
          : { json: { items: presets } },
      );
    if (url === "/api/novel-setups/preset-preview") {
      const input = request.postDataJSON();
      if (hold) await hold;
      return route.fulfill({
        json: {
          preset_id: input.preset_id,
          preset_revision: "2026-10-27.1",
          setup: { ...setup(), idea: input.idea || setup().idea },
          reader_promise: "每卷揭开一层真相",
          skill_names: ["novel-planning"],
          notes: ["静态建议"],
        },
      });
    }
    if (url === "/api/novel-setups")
      return route.fulfill({ json: { items: saved ? [saved] : [] } });
    if (url.endsWith("/assistant"))
      return route.fulfill({
        json: {
          project: { id: "private-project", name: "私人建书资料" },
          actor: { id: "owner", role: "owner" },
          branch: { id: "private-branch", name: "建书草稿", head: "head" },
        },
      });
    if (
      url === "/api/projects/private-project/resources" ||
      url.endsWith("/candidates")
    )
      return route.fulfill({ json: { items: [] } });
    if (url === "/api/projects/private-project/branches/private-branch")
      return route.fulfill({
        json: { id: "private-branch", name: "建书草稿", head: "head" },
      });
    if (url.endsWith("/confirm")) {
      confirms.push(request.postDataJSON());
      return route.fulfill({ status: 503, json: { error: "创建结果未知" } });
    }
    if (/\/api\/novel-setups\/[a-f0-9]{32}$/.test(url)) {
      if (request.method() === "PUT") {
        if (conflict && url.endsWith(options.initial?.id ?? "!"))
          return route.fulfill({
            status: 409,
            json: { error: "草稿版本冲突" },
          });
        const input = request.postDataJSON() as NovelDraft;
        const waiting = holdSave;
        holdSave = undefined;
        if (waiting) await waiting;
        saved = { ...input, revision: input.revision + 1 };
        storedDrafts.set(saved.id, saved);
      }
      return route.fulfill({ json: storedDrafts.get(url.split("/").at(-1)!) });
    }
    return route.fulfill({ status: 404, json: { error: `未预期接口 ${url}` } });
  });
  await page.goto("/");
  await page.getByRole("button", { name: "新建小说", exact: true }).click();
  return {
    calls,
    confirms,
    saved: () => saved,
    holdPreview: () => {
      hold = new Promise<void>((resolve) => {
        release = resolve;
      });
    },
    release: () => release?.(),
    holdNextSave: () => {
      holdSave = new Promise<void>((resolve) => {
        releaseSave = resolve;
      });
    },
    releaseSave: () => releaseSave?.(),
    conflict: () => {
      conflict = true;
    },
  };
}

test("一句话与预设直接得到完整可编辑方案，确认前不建书也不调用模型", async ({
  page,
}, testInfo) => {
  const state = await fixture(page);
  await expect(page.getByRole("button", { name: /选择预设：/ })).toHaveCount(8);
  await expect(
    page.getByRole("heading", { name: "男频常见方向" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "女频常见方向" }),
  ).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("presets.png"),
    fullPage: true,
  });
  await page.getByLabel("一句话创作想法").fill("修钟师找回被抹去的朋友");
  await page.getByRole("button", { name: "选择预设：都市事业" }).click();
  await page.getByRole("button", { name: "获取免费预设方案" }).click();
  await expect(
    page.getByText("预设草案，未调用AI", { exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "旧城钟声",
  );
  await expect(page.getByLabel("核心梗概或创作想法")).toHaveValue(
    "修钟师找回被抹去的朋友",
  );
  await expect(page.getByLabel("世界背景")).toBeVisible();
  await page
    .locator("summary")
    .filter({ hasText: /初始角色/ })
    .click();
  await expect(page.getByLabel("姓名或暂名")).toBeVisible();
  await page
    .locator("summary")
    .filter({ hasText: /叙事与篇幅/ })
    .click();
  await expect(page.getByLabel("目标总字数")).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("editable-plan.png"),
    fullPage: true,
  });
  expect(state.confirms).toHaveLength(0);
  expect(
    state.calls.some((call) => /generation|price-preview/.test(call)),
  ).toBe(false);
  await expect.poll(() => state.saved()?.setup.model_choice).toBe("later");
  await page.getByLabel("我确认采用以上小说设置").check();
  await page.getByRole("button", { name: "确认创建小说", exact: true }).click();
  await expect.poll(() => state.confirms.length).toBe(1);
});

test("旧五步草稿同页恢复，换预设明确采用范围并保留手改与高级设置", async ({
  page,
}) => {
  const initial: NovelDraft = {
    id: "a".repeat(32),
    revision: 3,
    step: 4,
    confirmed_novel_id: "",
    setup: {
      ...setup(),
      title: "作者手写书名",
      source_mode: "import",
      source_text: "作者原稿",
    },
  };
  const state = await fixture(page, { initial });
  await page.getByRole("button", { name: /作者手写书名.*继续编辑/ }).click();
  await expect(
    page.getByRole("button", { name: "选择预设：玄幻成长" }),
  ).not.toBeVisible();
  await page.getByText("调整预设方向与想法", { exact: true }).click();
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "作者手写书名",
  );
  await page
    .locator("summary")
    .filter({ hasText: /叙事与篇幅/ })
    .click();
  await expect(page.getByLabel("目标总字数")).toBeVisible();
  await page.getByRole("button", { name: "选择预设：玄幻成长" }).click();
  await page.getByRole("button", { name: "获取免费预设方案" }).click();
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "作者手写书名",
  );
  await page.getByRole("button", { name: "保留当前内容，只补充空白" }).click();
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "作者手写书名",
  );
  await page.getByRole("button", { name: "保存设置草稿", exact: true }).click();
  expect(state.saved()?.setup.source_text).toBe("作者原稿");
  expect(state.confirms).toHaveLength(0);
});

test("目录失败仍可自由填写与重试", async ({ page }) => {
  await fixture(page, { missingCatalog: true });
  await expect(
    page.getByRole("button", { name: "重试加载预设" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "自由填写方案" }).click();
  await page.getByLabel("书名", { exact: true }).fill("自由创作");
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "自由创作",
  );
});

test("预设晚到结果不会覆盖请求期间的手改", async ({ page }) => {
  const initial: NovelDraft = {
    id: "b".repeat(32),
    revision: 2,
    step: 2,
    confirmed_novel_id: "",
    setup: setup(),
  };
  const state = await fixture(page, { initial });
  await page.getByRole("button", { name: /旧城钟声.*继续编辑/ }).click();
  await page.getByText("调整预设方向与想法", { exact: true }).click();
  await page.getByRole("button", { name: "选择预设：都市事业" }).click();
  state.holdPreview();
  await page.getByRole("button", { name: "获取免费预设方案" }).click();
  await expect
    .poll(
      () =>
        state.calls.filter((call) => call.includes("preset-preview")).length,
    )
    .toBe(1);
  await page.getByLabel("书名", { exact: true }).fill("请求途中修改");
  state.release();
  await expect(page.getByRole("status")).toContainText("已保留");
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "请求途中修改",
  );
  await expect(
    page.getByRole("button", { name: "替换故事方案，保留高级设置" }),
  ).toHaveCount(0);
});

test("版本冲突保留文字并可另存副本恢复", async ({ page }) => {
  const initial: NovelDraft = {
    id: "c".repeat(32),
    revision: 2,
    step: 5,
    confirmed_novel_id: "",
    setup: setup(),
  };
  const state = await fixture(page, { initial });
  await page.getByRole("button", { name: /旧城钟声.*继续编辑/ }).click();
  state.conflict();
  await page.getByLabel("书名", { exact: true }).fill("冲突仍要保留");
  await page.getByRole("button", { name: "保存设置草稿", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "保留当前文字，另存恢复草稿" }),
  ).toBeVisible();
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "冲突仍要保留",
  );
  await page
    .getByRole("button", { name: "保留当前文字，另存恢复草稿" })
    .click();
  await expect.poll(() => state.saved()?.id).not.toBe(initial.id);
  expect(state.saved()?.setup.title).toBe("冲突仍要保留");
  expect(state.confirms).toHaveLength(0);
});

test("未选预设的一句话在返回书架时也保存为可恢复草稿", async ({ page }) => {
  const state = await fixture(page);
  await page.getByLabel("一句话创作想法").fill("书店只在失眠的人面前开门");
  const protectedExit = await page.evaluate(() => {
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    return event.defaultPrevented;
  });
  expect(protectedExit).toBe(true);
  await page.getByRole("button", { name: "返回书架", exact: true }).click();
  await expect
    .poll(() => state.saved()?.setup.idea)
    .toBe("书店只在失眠的人面前开门");
  expect(state.confirms).toHaveLength(0);
});

test("一句话与附件入口复用私人资料助手，原想法无需重复填写", async ({
  page,
}) => {
  const state = await fixture(page);
  await page
    .getByLabel("一句话创作想法")
    .fill("把我上传的旧城笔记完善成悬疑故事");
  await page.getByRole("button", { name: "用想法或附件让AI完善" }).click();
  await expect(page.getByLabel("我的想法与完善要求")).toHaveValue(
    "把我上传的旧城笔记完善成悬疑故事",
  );
  await expect(
    page.getByRole("button", { name: "上传与整理参考资料" }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "打开建书助手" }),
  ).toBeVisible();
  expect(state.saved()?.revision).toBeGreaterThan(0);
  expect(
    state.calls.some((call) => /generation|price-preview/.test(call)),
  ).toBe(false);
  expect(state.confirms).toHaveLength(0);
});

test("确认只授权当前内容，修改取消勾选，失败重试保留同一回执", async ({
  page,
}) => {
  const initial: NovelDraft = {
    id: "d".repeat(32),
    revision: 4,
    step: 5,
    confirmed_novel_id: "",
    setup: setup(),
  };
  const state = await fixture(page, { initial });
  await page.getByRole("button", { name: /旧城钟声.*继续编辑/ }).click();
  await page.getByLabel("我确认采用以上小说设置").check();
  await page.getByLabel("世界背景").fill("旧城与地下钟楼");
  await expect(page.getByLabel("我确认采用以上小说设置")).not.toBeChecked();
  await expect(
    page.getByRole("button", { name: "确认创建小说", exact: true }),
  ).toBeDisabled();
  await page.getByLabel("我确认采用以上小说设置").check();
  await page.getByRole("button", { name: "确认创建小说", exact: true }).click();
  await expect.poll(() => state.confirms.length).toBe(1);
  await expect(page.getByRole("alert")).toContainText("结果未知");
  await page.getByRole("button", { name: "确认创建小说", exact: true }).click();
  await expect.poll(() => state.confirms.length).toBe(2);
  expect(state.confirms[1]).toEqual(state.confirms[0]);
  expect(state.saved()?.setup.world).toBe("旧城与地下钟楼");
});

test("恢复旧草稿前先保留尚未采用预设的一句话", async ({ page }) => {
  const initial: NovelDraft = {
    id: "e".repeat(32),
    revision: 1,
    step: 3,
    confirmed_novel_id: "",
    setup: setup(),
  };
  const state = await fixture(page, { initial });
  await page.getByLabel("一句话创作想法").fill("先别覆盖这句新的灵感");
  await page.getByRole("button", { name: /旧城钟声.*继续编辑/ }).click();
  await expect
    .poll(() => state.saved()?.setup.idea)
    .toBe("先别覆盖这句新的灵感");
  await expect(page.getByLabel("核心梗概或创作想法")).toHaveValue(
    "修钟师寻找失踪朋友",
  );
});

test("进入助手等待保存时继续手改会留在原表单，重试才携带最新稿进入", async ({
  page,
}) => {
  const initial: NovelDraft = {
    id: "f".repeat(32),
    revision: 2,
    step: 5,
    confirmed_novel_id: "",
    setup: setup(),
  };
  const state = await fixture(page, { initial });
  await page.getByRole("button", { name: /旧城钟声.*继续编辑/ }).click();
  state.holdNextSave();
  await page.getByLabel("书名", { exact: true }).fill("请求前的书名");
  await page
    .getByRole("button", { name: "用想法或附件让AI完善", exact: true })
    .click();
  await expect
    .poll(
      () =>
        state.calls.filter(
          (call) => call === `PUT /api/novel-setups/${initial.id}`,
        ).length,
    )
    .toBe(1);
  try {
    await page
      .getByLabel("书名", { exact: true })
      .fill("保存途中继续修改的书名");
    await page.getByLabel("世界背景").fill("保存途中补充的新世界设定");
  } finally {
    state.releaseSave();
  }
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "保存途中继续修改的书名",
  );
  await expect(page.getByLabel("世界背景")).toHaveValue(
    "保存途中补充的新世界设定",
  );
  await expect(page.getByRole("status")).toContainText("已保留");
  expect(
    state.calls.filter((call) => call.endsWith("/assistant")),
  ).toHaveLength(0);
  await page
    .getByRole("button", { name: "用想法或附件让AI完善", exact: true })
    .click();
  await expect(page.getByLabel("我的想法与完善要求")).toBeVisible();
  expect(state.saved()?.setup.title).toBe("保存途中继续修改的书名");
  expect(state.saved()?.setup.world).toBe("保存途中补充的新世界设定");
});
