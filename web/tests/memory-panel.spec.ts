import { test, expect, type Page } from "@playwright/test";
import type {
  MemoryItem,
  MemoryPack,
  MemorySession,
} from "../src/features/ip/memory";

async function openMemorySection(
  page: Page,
  name: "记忆浏览" | "整理候选" | "短期会话" | "写作材料",
) {
  await page
    .getByRole("navigation", { name: "创作记忆分区", exact: true })
    .getByRole("button", { name, exact: true })
    .click();
}

const source = {
  id: "resource-1",
  name: "人物前史",
  head: "resource-revision-1",
  hash: "a".repeat(64),
  text: "序😀林舟守住承诺。尾声",
};
const session: MemorySession = {
  id: "session-1",
  revision: 1,
  document: { title: "第一章规划", goal: "", notes: "", focus_ids: [] },
};
const itemBase: MemoryItem = {
  id: "memory-old",
  revision: "memory-revision-1",
  project: "project-1",
  branch: "branch-a",
  kind: "summary",
  state: "stale",
  visibility: "author",
  from: 0,
  reveal_at: 0,
  text: "原始摘要已过期",
  score: 0,
  sources: [
    {
      resource: source.id,
      revision: "old-source",
      hash: source.hash,
      start_rune: 2,
      end_rune: 9,
    },
  ],
};
function packFor(
  items: MemoryItem[],
  request: Record<string, unknown>,
): MemoryPack {
  return {
    request: {
      scope: {
        project: "project-1",
        branch: "branch-a",
        canon: "canon-1",
        actor: "actor-1",
        permission_epoch: "1",
        purpose: "author",
        perspective: "",
        story_time: 0,
        narrative_time: 0,
        session: String(request.session ?? ""),
        timeline: "main",
      },
      budget: 65536,
      output_reserve: 8192,
      overhead_reserve: 1024,
      source_epoch: "1",
      compiler_version: "creative-memory-v1",
    },
    items: items
      .filter((item) => item.state !== "stale" && item.state !== "revoked")
      .map((item) => ({
        item,
        channel: item.control ? "control" : "evidence",
        reason: "required_or_dependency",
      })),
    omissions: [{ id: "memory-old", reason: "budget" }],
    omitted_count: 20,
    estimated_tokens: 3000,
    hash: "b".repeat(64),
  };
}
async function mockResources(page: Page) {
  await page.route("**/api/projects/project-1/resources**", async (route) => {
    const url = new URL(route.request().url());
    await route.fulfill({
      json: url.pathname.endsWith(`/${source.id}`)
        ? source
        : { items: [source] },
    });
  });
}

test("资料整理先准备后启动，选中结果导入私人候选且保留编辑入口", async ({
  page,
}) => {
  await mockResources(page);
  const records: MemoryItem[] = [];
  let dispatches = 0;
  let imports = 0;
  let contextReads = 0;
  const preparations: string[] = [];
  let run: Record<string, unknown> | null = null;
  await page.route("**/api/projects/project-1/**", async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (path.includes("/resources"))
      return route.fulfill({ json: { items: [source] } });
    if (path.endsWith("/generation-options"))
      return route.fulfill({
        json: {
          items: [
            {
              role: "bookkeeper",
              name: "整理模型",
              model: "test-model",
              price: { id: "price-1", currency: "USD" },
            },
          ],
        },
      });
    if (path.endsWith("/presets/memory"))
      return route.fulfill({ json: { root_id: "recipe-1" } });
    if (path.endsWith("/extraction-input")) {
      expect(req.postDataJSON().resource_ids).toEqual([source.id]);
      expect(req.postDataJSON().instructions).toBe("保留梦境和人物信息差");
      return route.fulfill({
        json: {
          schema: 1,
          project_id: "project-1",
          base_revision: "canon-1",
          sources: [source],
        },
      });
    }
    if (path.endsWith("/knowledge/search")) {
      contextReads++;
      expect(req.postDataJSON().purpose).toBe("critic");
      return route.fulfill({ json: { id: "context-1" } });
    }
    if (path.endsWith("/recipe-executions") && req.method() === "POST") {
      const body = req.postDataJSON();
      preparations.push(body.operation_id);
      expect(body.steps[0].context_id).toBe("context-1");
      expect(body.steps[0].input).toEqual(body.input);
      expect(body.budget_limit_micros).toBe(2000000);
      run = {
        id: "run-1",
        revision: 1,
        state: "PREPARED",
        branch_id: "branch-a",
        currency: "USD",
        budget_limit_micros: 2000000,
        spent_micros: 0,
        frozen: {
          definition: {
            definition: { key: "shared-memory-consolidation-recipe" },
          },
        },
        steps: [],
      };
      return route.fulfill({ json: run });
    }
    if (path.endsWith("/recipe-executions"))
      return route.fulfill({ json: { items: run ? [run] : [] } });
    if (path.endsWith("/run-1/start")) {
      dispatches++;
      run = {
        ...run,
        revision: 2,
        state: "WAITING_CONFIRMATION",
        steps: [
          {
            id: "step-1",
            key: "memory-consolidation",
            active: true,
            state: "WAITING_CONFIRMATION",
            output_hash: "b".repeat(64),
            output: {
              candidates: [
                {
                  id: "c1",
                  kind: "belief",
                  context: "dream",
                  disposition: "needs_confirmation",
                  statement: "林舟梦见钥匙已经丢失",
                  evidence: [{ quote: "他梦见空空的衣袋。" }],
                },
              ],
            },
          },
        ],
      };
      return route.fulfill({ json: run });
    }
    if (path.endsWith("/recipe-executions/run-1"))
      return route.fulfill({ json: run });
    if (path.endsWith("/run-1/confirm")) {
      expect(req.postDataJSON().step_key).toBe("memory-consolidation");
      expect(req.postDataJSON().output_hash).toBe("b".repeat(64));
      run = { ...run, state: "COMPLETED", revision: 3 };
      return route.fulfill({ json: run });
    }
    if (path.endsWith("/import-extraction")) {
      const body = req.postDataJSON();
      expect(body.candidate_ids).toEqual(["c1"]);
      expect(body.session_id).toBe(session.id);
      expect(body.expected_revision).toBe(2);
      imports++;
      records.push({
        ...itemBase,
        id: "imported-1",
        state: "candidate",
        kind: "hypothesis",
        actor: "actor-1",
        session: session.id,
        text: "林舟梦见钥匙已经丢失",
      });
      return route.fulfill({ json: { items: records } });
    }
    if (path.endsWith("/memory"))
      return route.fulfill({ json: { items: records, sessions: [session] } });
    return route.fulfill({ status: 404, json: { error: "未实现的测试请求" } });
  });
  await page.goto("/tests/fixtures/memory-panel.html");
  await page.getByLabel("当前工作会话").selectOption(session.id);
  await openMemorySection(page, "整理候选");
  const panel = page.getByTestId("memory-extraction");
  await panel.getByText("从作品资料整理记忆", { exact: true }).click();
  await panel.getByLabel("人物前史", { exact: true }).check();
  await panel
    .getByLabel("整理要求", { exact: true })
    .fill("保留梦境和人物信息差");
  await panel.getByLabel(/本次预算上限/).fill("2");
  await panel
    .getByRole("button", { name: "准备整理任务", exact: true })
    .click();
  await expect(
    panel.getByRole("button", { name: "按已设预算开始整理", exact: true }),
  ).toBeVisible();
  expect(dispatches).toBe(0);
  await panel
    .getByRole("button", { name: "准备整理任务", exact: true })
    .click();
  await expect.poll(() => preparations.length).toBe(2);
  expect(preparations[0]).toBe(preparations[1]);
  expect(contextReads).toBe(1);
  await panel
    .getByRole("button", { name: "按已设预算开始整理", exact: true })
    .click();
  await panel.getByLabel("林舟梦见钥匙已经丢失", { exact: true }).check();
  await panel
    .getByRole("button", { name: "将选中结果加入私人候选", exact: true })
    .click();
  await openMemorySection(page, "记忆浏览");
  await expect(
    page.getByText("推测 · 未确认候选", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "编辑记忆", exact: true }),
  ).toBeVisible();
  expect(dispatches).toBe(1);
  expect(imports).toBe(1);
  await openMemorySection(page, "整理候选");
  await panel
    .getByRole("button", {
      name: "完成本次整理审阅（不自动采用）",
      exact: true,
    })
    .click();
  await expect(
    panel.getByRole("button", {
      name: "完成本次整理审阅（不自动采用）",
      exact: true,
    }),
  ).toHaveCount(0);
  expect(records[0]?.state).toBe("candidate");
});

test("候选保留来源、显式采用与撤回，编辑共享项需重新采用，编译可查看原文", async ({
  page,
}) => {
  const records = [structuredClone(itemBase)];
  const saves: {
    item: MemoryItem;
    expected_revision: string;
    operation_id: string;
  }[] = [];
  const actions: string[] = [];
  await mockResources(page);
  await page.route(
    "**/api/projects/project-1/branches/*/memory**",
    async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (request.method() === "GET") {
        await route.fulfill({ json: { items: records, sessions: [session] } });
        return;
      }
      if (url.pathname.endsWith("/items")) {
        const body = request.postDataJSON();
        saves.push(body);
        const saved: MemoryItem = {
          ...body.item,
          id: body.item.id || "memory-1",
          revision: `saved-${saves.length}`,
          actor: "actor-1",
          state: "candidate",
        };
        const index = records.findIndex((item) => item.id === saved.id);
        if (index >= 0) records[index] = saved;
        else records.push(saved);
        await route.fulfill({ json: saved });
        return;
      }
      if (/\/(adopt|revoke)$/.test(url.pathname)) {
        const action = url.pathname.split("/").at(-1)!;
        actions.push(action);
        const current = records.find((item) => item.id === "memory-1")!;
        expect(request.postDataJSON().expected_revision).toBe(current.revision);
        current.state = action === "adopt" ? "adopted_plan" : "revoked";
        current.revision = `${action}-${actions.length}`;
        delete current.actor;
        delete current.session;
        await route.fulfill({ json: current });
        return;
      }
      if (url.pathname.endsWith("/compile")) {
        await route.fulfill({ json: packFor(records, request.postDataJSON()) });
        return;
      }
      await route.fulfill({ status: 404, json: { error: "未实现的测试请求" } });
    },
  );
  await page.goto("/tests/fixtures/memory-panel.html");
  await expect(
    page.getByText("来源过期，请修订来源并重新确认。"),
  ).toBeVisible();
  await page.getByLabel("当前工作会话").selectOption("session-1");
  await page.getByRole("button", { name: "新增记忆候选", exact: true }).click();
  await page
    .getByLabel("记忆内容", { exact: true })
    .fill("即使付出代价，也要守住承诺。");
  await expect(
    page.getByRole("button", { name: "模拟切换分支", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("combobox", { name: "来源资料", exact: true })
    .selectOption(source.id);
  await page.getByLabel("引用原文片段", { exact: true }).fill("林舟守住承诺。");
  await page.getByRole("button", { name: "加入来源片段" }).click();
  await expect(page.getByTestId("memory-editor")).toContainText("第 3—9 字");
  await page.getByRole("button", { name: "保存记忆候选", exact: true }).click();
  await expect(page.getByTestId("memory-item-memory-1")).toContainText(
    "未确认候选",
  );
  expect(actions).toEqual([]);
  expect(saves[0].item.sources[0]).toEqual({
    resource: source.id,
    revision: source.head,
    hash: source.hash,
    start_rune: 2,
    end_rune: 9,
  });
  expect(saves[0].item.session).toBe(session.id);
  expect(saves[0].expected_revision).toBe("");
  await page.getByRole("button", { name: "检查并采用记忆" }).click();
  expect(actions).toEqual([]);
  await page
    .getByRole("button", { name: "确认采用这条记忆", exact: true })
    .click();
  await expect(page.getByTestId("memory-item-memory-1")).toContainText(
    "已采用的创作约束",
  );
  await openMemorySection(page, "写作材料");
  await page.getByRole("button", { name: "编译上下文", exact: true }).click();
  await expect(page.getByTestId("memory-pack")).toContainText("共 20 条");
  await expect(page.getByTestId("memory-pack")).toContainText(
    "仅列出 1 条可见遗漏样本",
  );
  await expect(page.getByTestId("memory-pack")).toContainText(
    "控制通道 · 隐藏创作约束",
  );
  await page.getByRole("button", { name: "查看已核对原文" }).click();
  await expect(page.getByTestId("memory-source")).toContainText(
    "林舟守住承诺。",
  );
  await openMemorySection(page, "记忆浏览");
  await page
    .getByTestId("memory-item-memory-1")
    .getByRole("button", { name: "编辑记忆" })
    .click();
  await page
    .getByLabel("记忆内容", { exact: true })
    .fill("承诺需要付出真实代价。");
  await page.getByRole("button", { name: "保存记忆候选", exact: true }).click();
  await expect(page.getByTestId("memory-item-memory-1")).toContainText(
    "未确认候选",
  );
  expect(saves[1].item.id).toBe("memory-1");
  expect(saves[1].expected_revision).toBe("adopt-1");
  expect(saves[1].operation_id).not.toBe(saves[0].operation_id);
  expect(actions).toEqual(["adopt"]);
  await expect(page.getByTestId("memory-pack")).toContainText(
    "此结果仅供历史查看",
  );
  await page
    .getByTestId("memory-item-memory-1")
    .getByRole("button", { name: "撤回记忆", exact: true })
    .click();
  expect(actions).toEqual(["adopt"]);
  await page
    .getByRole("button", { name: "确认撤回这条记忆", exact: true })
    .click();
  await expect(page.getByTestId("memory-item-memory-1")).toContainText(
    "已撤回",
  );
  await openMemorySection(page, "写作材料");
  await page
    .getByTestId("memory-pack")
    .screenshot({ path: "test-results/memory-panel/verified-panel.png" });
});

test("会话创建与更新使用原始修订，刷新和冲突保留未保存笔记", async ({
  page,
}) => {
  let savedSession: MemorySession | null = null;
  const writes: {
    id: string;
    expected_revision: number;
    document: MemorySession["document"];
  }[] = [];
  await mockResources(page);
  await page.route(
    "**/api/projects/project-1/branches/*/memory**",
    async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({
          json: { items: [], sessions: savedSession ? [savedSession] : [] },
        });
        return;
      }
      const body = route.request().postDataJSON();
      writes.push(body);
      if (writes.length === 1) {
        savedSession = {
          id: "session-new",
          revision: 1,
          document: body.document,
        };
        await route.fulfill({ json: savedSession });
      } else {
        await route.fulfill({
          status: 409,
          json: { error: "工作记录已有新版本，请检查后再保存" },
        });
      }
    },
  );
  await page.goto("/tests/fixtures/memory-panel.html");
  await page.getByRole("button", { name: "新建工作会话", exact: true }).click();
  await page.getByLabel("工作会话名称").fill("本章创作");
  await page.getByLabel("工作笔记").fill("已保存笔记");
  await page.getByRole("button", { name: "创建工作会话", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("工作记录已保存");
  expect(writes[0].id).toBe("");
  expect(writes[0].expected_revision).toBe(0);
  await page.getByLabel("工作笔记").fill("本地未保存的细节");
  savedSession = {
    id: "session-new",
    revision: 2,
    document: { title: "远端修订", goal: "", notes: "远端笔记", focus_ids: [] },
  };
  await page.getByRole("button", { name: "刷新记忆列表", exact: true }).click();
  await expect(page.getByLabel("工作笔记")).toHaveValue("本地未保存的细节");
  await page.getByRole("button", { name: "保存工作记录", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("工作记录已有新版本");
  expect(writes[1].id).toBe("session-new");
  expect(writes[1].expected_revision).toBe(1);
  await expect(page.getByLabel("工作笔记")).toHaveValue("本地未保存的细节");
});

test("分支卸载后迟到的编译结果不会进入新分支", async ({ page }) => {
  let release = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let started = false;
  await mockResources(page);
  await page.route(
    "**/api/projects/project-1/branches/*/memory**",
    async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({ json: { items: [], sessions: [session] } });
        return;
      }
      started = true;
      await gate;
      await route.fulfill({
        json: packFor(
          [
            {
              ...itemBase,
              state: "published",
              text: "旧分支秘密",
              sources: [],
            },
          ],
          {},
        ),
      });
    },
  );
  await page.goto("/tests/fixtures/memory-panel.html");
  await openMemorySection(page, "写作材料");
  await page.getByRole("button", { name: "编译上下文", exact: true }).click();
  await expect.poll(() => started).toBe(true);
  await page.getByRole("button", { name: "模拟外部切换" }).click();
  await openMemorySection(page, "写作材料");
  await expect(
    page.getByRole("button", { name: "编译上下文", exact: true }),
  ).toBeEnabled();
  release();
  await expect(page.getByText("旧分支秘密", { exact: true })).toHaveCount(0);
  await expect(page.getByTestId("memory-pack")).toHaveCount(0);
});
