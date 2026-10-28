import { test, expect } from "@playwright/test";
import { createHash } from "node:crypto";
import type { MemoryItem } from "../src/features/ip/memory";

for (const timeline of ["main", "past"]) {
  test(`增量待办锁定原摘要身份，父级等待确认，准备使用专用修复范围 ${timeline}`, async ({
    page,
  }) => {
    const target = {
      item_id: "volume-1",
      revision: "old-revision",
      level: "volume",
      unit_id: "same-unit",
      title: "第一卷",
      timeline,
    };
    let revisionChanged = false;
    let extraction: Record<string, unknown> | null = null;
    let contextInput: Record<string, unknown> | null = null;
    await page.route("**/api/projects/project-1/**", async (route) => {
      const req = route.request(),
        path = new URL(req.url()).pathname;
      if (path.endsWith("/resources"))
        return route.fulfill({
          json: {
            items: [{ id: "raw-1", head: "latest", name: "第一章原文" }],
          },
        });
      if (path.endsWith("/memory"))
        return route.fulfill({
          json: {
            items: [
              {
                id: "chapter-1",
                revision: "child-r1",
                project: "project-1",
                branch: "branch-a",
                kind: "summary",
                state: "published",
                visibility: "author",
                from: 0,
                reveal_at: 0,
                text: "章节摘要",
                sources: [],
                score: 0,
                timeline,
                summary: {
                  level: "chapter",
                  unit_id: "child-unit",
                  title: "第一章",
                },
              },
            ],
            sessions: [],
            refresh_tasks: [
              {
                id: revisionChanged ? "changed-task" : "ready-task",
                state: "READY",
                reason: "",
                input: {
                  resource_ids: [],
                  memory_ids: ["chapter-1"],
                  story_time: 8,
                  summary_target: {
                    ...target,
                    revision: revisionChanged
                      ? "new-revision"
                      : target.revision,
                  },
                },
              },
              {
                id: "waiting-task",
                state: "WAITING_INPUT",
                reason: "下层摘要尚未重新确认或不可用",
                input: {
                  story_time: 8,
                  summary_target: {
                    ...target,
                    item_id: "book-1",
                    level: "book",
                    title: "全书",
                  },
                },
              },
            ],
          },
        });
      if (path.endsWith("/generation-options"))
        return route.fulfill({
          json: {
            items: [
              {
                role: "bookkeeper",
                name: "整理模型",
                model: "test",
                price: { id: "price-1", currency: "USD" },
              },
            ],
          },
        });
      if (path.endsWith("/recipe-executions"))
        return route.fulfill({ json: { items: [] } });
      if (path.endsWith("/extraction-input")) {
        extraction = req.postDataJSON();
        return route.fulfill({ json: extraction });
      }
      if (path.endsWith("/presets/memory"))
        return route.fulfill({ json: { root_id: "recipe-1" } });
      if (path.endsWith("/knowledge/search")) {
        contextInput = req.postDataJSON();
        return route.fulfill({
          status: 409,
          json: { error: "测试仅检查准备参数，不开始模型任务" },
        });
      }
      return route.fulfill({ json: { items: [] } });
    });
    await page.goto("/tests/fixtures/memory-panel.html");
    const panel = page.getByTestId("memory-extraction");
    await panel.getByText("从作品资料整理记忆", { exact: true }).click();
    await expect(
      panel.locator('option[value="waiting-task"]'),
    ).toHaveJSProperty("disabled", true);
    await panel.getByLabel("增量重整").selectOption("ready-task");
    await expect(panel.getByLabel("摘要名称")).toHaveValue("第一卷");
    await expect(panel.getByLabel("整理方式")).toBeDisabled();
    await expect(panel.getByLabel("第一章", { exact: true })).toBeChecked();
    await expect(panel.getByLabel("第一章", { exact: true })).toBeDisabled();
    await panel.getByLabel(/本次预算上限/).fill("1");
    await panel
      .getByRole("button", { name: "准备整理任务", exact: true })
      .click();
    await expect.poll(() => contextInput).not.toBeNull();
    expect(extraction).toMatchObject({
      summary_target: target,
      resource_ids: [],
      memory_ids: ["chapter-1"],
      story_time: 8,
    });
    expect(contextInput).toMatchObject({
      purpose: "critic",
      memory: {
        timeline,
        refresh: { id: target.item_id, revision: target.revision },
      },
    });
    revisionChanged = true;
    extraction = null;
    await page.reload();
    await panel.getByText("从作品资料整理记忆", { exact: true }).click();
    await expect(panel.getByLabel("增量重整")).toHaveValue("ready-task");
    await panel
      .getByRole("button", { name: "准备整理任务", exact: true })
      .click();
    await expect(panel.getByRole("alert")).toContainText("重整条件已变化");
    expect(extraction).toBeNull();
  });
}

test("分卷整理选择下层摘要，历史来源可下钻，手改保留结构化来源", async ({
  page,
}) => {
  const original = "林舟走进雾城，救出同伴。";
  const sourceHash = createHash("sha256").update(original).digest("hex");
  const chapter: MemoryItem = {
    id: "chapter-1",
    revision: "chapter-r1",
    project: "project-1",
    branch: "branch-a",
    kind: "summary",
    state: "published",
    visibility: "author",
    from: 0,
    reveal_at: 0,
    text: "章节旧摘要：救出同伴。",
    sources: [
      {
        resource: "raw-1",
        revision: "release-old",
        hash: sourceHash,
        start_rune: 0,
        end_rune: Array.from(original).length,
      },
    ],
    score: 0,
    summary: { level: "chapter", unit_id: "unit-1", title: "第一章" },
  };
  const pin = {
    id: chapter.id,
    revision: chapter.revision,
    hash: createHash("sha256").update(chapter.text).digest("hex"),
  };
  const volume: MemoryItem = {
    ...chapter,
    id: "volume-1",
    revision: "volume-r1",
    state: "candidate",
    actor: "actor-1",
    session: "session-1",
    text: "分卷候选摘要",
    sources: [],
    summary: {
      level: "volume",
      unit_id: "unit-2",
      title: "第一卷",
      inputs: [pin],
    },
  };
  let prepared: Record<string, unknown> | null = null;
  let saved: MemoryItem | null = null;
  await page.route("**/api/projects/project-1/**", async (route) => {
    const req = route.request(),
      path = new URL(req.url()).pathname;
    if (path.endsWith("/resources"))
      return route.fulfill({ json: { items: [] } });
    if (path.endsWith("/resources/raw-1"))
      return route.fulfill({
        json: {
          id: "raw-1",
          name: "章节原文",
          head: "release-new",
          hash: "f".repeat(64),
          text: "新版",
        },
      });
    if (path.endsWith("/releases/release-old"))
      return route.fulfill({
        json: {
          id: "release-old",
          status: "PUBLISHED",
          manifest: {
            branch_id: "branch-a",
            artifacts: [{ resource_id: "raw-1", hash: sourceHash }],
          },
          contents: [{ resource_id: "raw-1", text: original }],
        },
      });
    if (path.endsWith("/generation-options"))
      return route.fulfill({
        json: {
          items: [
            {
              role: "bookkeeper",
              name: "整理模型",
              model: "test",
              price: { id: "price-1", currency: "USD" },
            },
          ],
        },
      });
    if (path.endsWith("/recipe-executions"))
      return route.fulfill({ json: { items: [] } });
    if (path.endsWith("/extraction-input")) {
      prepared = req.postDataJSON();
      return route.fulfill({
        status: 409,
        json: { error: "测试在准备边界停止" },
      });
    }
    if (path.includes("/revisions/")) return route.fulfill({ json: chapter });
    if (path.endsWith("/items") && req.method() === "POST") {
      saved = req.postDataJSON().item;
      return route.fulfill({ json: { ...saved, revision: "volume-r2" } });
    }
    if (path.endsWith("/memory"))
      return route.fulfill({
        json: {
          items: [chapter, volume],
          sessions: [
            {
              id: "session-1",
              revision: 1,
              document: { title: "规划", goal: "", notes: "", focus_ids: [] },
            },
          ],
        },
      });
    return route.fulfill({ json: { items: [] } });
  });
  await page.goto("/tests/fixtures/memory-panel.html");
  await page.getByLabel("当前工作会话").selectOption("session-1");
  const panel = page.getByTestId("memory-extraction");
  await panel.getByText("从作品资料整理记忆", { exact: true }).click();
  await panel.getByLabel("整理方式").selectOption("volume");
  await panel.getByLabel("摘要名称").fill("第一卷·雾城");
  await panel.getByLabel("第一章", { exact: true }).check();
  await panel.getByLabel(/本次预算上限/).fill("1");
  await panel
    .getByRole("button", { name: "准备整理任务", exact: true })
    .click();
  await expect.poll(() => prepared).not.toBeNull();
  expect(prepared).toMatchObject({
    resource_ids: [],
    memory_ids: ["chapter-1"],
    summary_target: { level: "volume", title: "第一卷·雾城", timeline: "main" },
  });
  const card = page.getByTestId("memory-item-volume-1");
  await card
    .getByRole("button", { name: "查看下层摘要 第一章", exact: true })
    .click();
  await expect(page.getByTestId("memory-source")).toContainText(chapter.text);
  await page
    .getByTestId("memory-source")
    .getByRole("button", { name: "查看原文片段 1" })
    .click();
  await expect(page.getByTestId("memory-source")).toContainText(
    "引用时的历史原文",
  );
  await expect(page.getByTestId("memory-source")).toContainText(original);
  await card.getByRole("button", { name: "编辑记忆", exact: true }).click();
  const editor = page.getByTestId("memory-editor");
  await expect(editor.getByLabel("记忆类别")).toBeDisabled();
  await editor.getByLabel("记忆内容").fill("作者补充的分卷摘要");
  await editor.getByRole("button", { name: /保存.*候选/ }).click();
  await expect.poll(() => saved).not.toBeNull();
  expect(saved).toMatchObject({
    text: "作者补充的分卷摘要",
    sources: [],
    summary: { level: "volume", inputs: [pin] },
  });
});
