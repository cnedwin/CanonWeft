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
const complete = (): NovelDraft => ({
  id: "a".repeat(32),
  revision: 1,
  step: 5,
  confirmed_novel_id: "",
  setup: {
    ...defaults,
    title: "雾城药师",
    genre: "悬疑",
    idea: "医者调查雾城旧案",
    conflict: "救人还是揭开真相",
    world: "雾城",
    character_mode: "manual",
    characters: [
      {
        name: "林舟",
        identity: "医师",
        goal: "找到失踪师父",
        background: "雾城长大",
        personality: "谨慎",
      },
    ],
    creation_mode: "manual",
  },
});
function deferred() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { promise, release };
}
async function fixture(page: Page, initial?: NovelDraft) {
  let saved = initial ? structuredClone(initial) : undefined;
  const confirms: unknown[] = [];
  const puts: NovelDraft[] = [];
  let hold: ReturnType<typeof deferred> | undefined;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/setup")
      return route.fulfill({ json: { required: false } });
    if (path === "/api/me")
      return route.fulfill({
        json: {
          id: "owner",
          workspace_id: "workspace",
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
        "/api/novel-setups/presets",
      ].includes(path)
    )
      return route.fulfill({ json: { items: [] } });
    if (path === "/api/novel-setups/defaults")
      return route.fulfill({ json: defaults });
    if (path === "/api/novel-setups")
      return route.fulfill({ json: { items: saved ? [saved] : [] } });
    if (path.endsWith("/confirm")) {
      confirms.push(request.postDataJSON());
      return route.fulfill({
        status: 503,
        json: { error: "模拟创建结果未知，请重试同一次确认" },
      });
    }
    if (/\/api\/novel-setups\/[a-f0-9]{32}$/.test(path)) {
      if (request.method() === "PUT") {
        const input = request.postDataJSON() as NovelDraft;
        puts.push(input);
        const waiting = hold;
        hold = undefined;
        if (waiting) await waiting.promise;
        saved = { ...input, revision: input.revision + 1 };
      }
      return route.fulfill({ json: saved });
    }
    return route.fulfill({
      status: 404,
      json: { error: `未预期接口 ${path}` },
    });
  });
  await page.goto("/");
  await page.getByRole("button", { name: "新建小说", exact: true }).click();
  if (initial)
    await page
      .getByRole("button", {
        name: new RegExp(`${initial.setup.title}.*继续编辑`),
      })
      .click();
  else await page.getByRole("button", { name: "自由填写方案" }).click();
  return {
    puts,
    confirms,
    holdNext: () => (hold = deferred()),
    saved: () => saved,
  };
}

test("确认期间锁定已审阅的设置，失败重试沿用同一回执", async ({ page }) => {
  const state = await fixture(page, complete());
  await page.getByText("高级设置：模型用途与核心保护", { exact: true }).click();
  await page.getByLabel("核心设定保护").selectOption("review_all_changes");
  await page.getByLabel("我确认采用以上小说设置").check();
  const wait = state.holdNext();
  await page.getByRole("button", { name: "确认创建小说", exact: true }).click();
  await expect.poll(() => state.puts.length).toBe(1);
  try {
    await expect(page.getByLabel("核心设定保护")).toBeDisabled();
    expect(state.confirms).toHaveLength(0);
  } finally {
    wait.release();
  }
  await expect(page.getByRole("alert")).toContainText("结果未知");
  await page.getByRole("button", { name: "确认创建小说", exact: true }).click();
  await expect.poll(() => state.confirms.length).toBe(2);
  expect(state.confirms[1]).toEqual(state.confirms[0]);
  expect(state.saved()?.setup.protection).toBe("review_all_changes");
});

test("自动保存期间继续输入不被旧回执覆盖，离开前保护未保存文字", async ({
  page,
}) => {
  const state = await fixture(page);
  await expect(
    page.getByText("私人建书草稿 ·", { exact: false }),
  ).toContainText("草稿已保存");
  const wait = state.holdNext();
  await page.getByLabel("书名", { exact: true }).fill("雾城");
  await expect.poll(() => state.puts.length).toBe(2);
  await page.getByLabel("书名", { exact: true }).fill("雾城药师");
  try {
    const protectedExit = await page.evaluate(() => {
      const event = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(event);
      return event.defaultPrevented;
    });
    expect(protectedExit).toBe(true);
  } finally {
    wait.release();
  }
  await expect.poll(() => state.saved()?.setup.title).toBe("雾城药师");
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "雾城药师",
  );
  await page.getByRole("button", { name: "保存并返回书架" }).click();
  await page.getByRole("button", { name: "新建小说", exact: true }).click();
  await page.getByRole("button", { name: /雾城药师.*继续编辑/ }).click();
  await expect(page.getByLabel("书名", { exact: true })).toHaveValue(
    "雾城药师",
  );
  expect(state.confirms).toHaveLength(0);
});

test("切换人工创作清除隐藏的模型选择", async ({ page }) => {
  const initial = complete();
  initial.setup.creation_mode = "assisted";
  initial.setup.model_choice = "configured";
  initial.setup.connection_id = "b".repeat(32);
  const state = await fixture(page, initial);
  await page.getByText("高级设置：模型用途与核心保护", { exact: true }).click();
  await page
    .getByRole("combobox", { name: /^创作方式/ })
    .selectOption("manual");
  await page.getByRole("button", { name: "保存设置草稿", exact: true }).click();
  await expect.poll(() => state.saved()?.setup.model_choice).toBe("");
  expect(state.saved()?.setup.connection_id).toBe("");
});
