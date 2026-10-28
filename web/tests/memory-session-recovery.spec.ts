import { test, expect } from "@playwright/test";
import type { MemorySession } from "../src/features/ip/memory";

test("expired session preserves local edits for an explicit new session", async ({
  page,
}) => {
  let expired = false;
  const writes: Record<string, unknown>[] = [];
  let session: MemorySession = {
    id: "session-old",
    revision: 1,
    document: { title: "卷一写作", goal: "", notes: "原笔记", focus_ids: [] },
  };
  await page.route("**/api/**", async (route) => {
    const request = route.request(),
      path = new URL(request.url()).pathname;
    if (request.method() === "POST" && path.endsWith("/sessions")) {
      const body = request.postDataJSON();
      writes.push(body);
      session = { id: "session-new", revision: 1, document: body.document };
      expired = false;
      return route.fulfill({ json: session });
    }
    if (path.endsWith("/memory"))
      return route.fulfill({
        json: {
          items: [],
          sessions: [
            expired
              ? {
                  ...session,
                  document: { title: "", goal: "", notes: "", focus_ids: [] },
                  retention: {
                    state: "EXPIRED",
                    expired_at: "2026-10-26T00:00:00Z",
                  },
                }
              : session,
          ],
        },
      });
    return route.fulfill({ json: { items: [] } });
  });
  await page.goto("/tests/fixtures/memory-panel.html");
  await page.getByLabel("当前工作会话").selectOption("session-old");
  await page.getByLabel("工作笔记").fill("本机尚未保存的高光桥段");
  expired = true;
  await page.reload();
  await expect(
    page.getByText("原工作会话已过期", { exact: false }),
  ).toBeVisible();
  await expect(page.getByLabel("工作笔记")).toBeDisabled();
  await expect(page.getByLabel("工作笔记")).toHaveValue(
    "本机尚未保存的高光桥段",
  );
  expect(writes).toHaveLength(0);
  await page
    .getByRole("button", { name: "保留输入，转为新会话", exact: true })
    .click();
  await expect(page.getByLabel("工作笔记")).toBeEnabled();
  await expect(page.getByLabel("工作笔记")).toHaveValue(
    "本机尚未保存的高光桥段",
  );
  await page.getByRole("button", { name: "创建工作会话", exact: true }).click();
  await expect.poll(() => writes.length).toBe(1);
  expect(writes[0]?.id).toBe("");
  expect(writes[0]?.expected_revision).toBe(0);
});

test("late save cannot replace the new branch and retains its recovery request", async ({
  page,
}) => {
  let release = () => {};
  let started = false;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/**", async (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      started = true;
      await gate;
      return route.fulfill({
        json: { id: "late-session", revision: 1, document: body.document },
      });
    }
    return route.fulfill({ json: { items: [], sessions: [] } });
  });
  await page.goto("/tests/fixtures/memory-panel.html");
  await page.getByRole("button", { name: "新建工作会话", exact: true }).click();
  await page.getByLabel("工作会话名称").fill("旧分支工作");
  await page.getByRole("button", { name: "创建工作会话", exact: true }).click();
  await expect.poll(() => started).toBe(true);
  await page.getByRole("button", { name: "模拟外部切换", exact: true }).click();
  release();
  await expect(
    page.getByRole("button", { name: "新建工作会话", exact: true }),
  ).toBeEnabled();
  await expect(page.getByLabel("工作会话名称")).toHaveCount(0);
  const records = await page.evaluate(() =>
    Object.entries(localStorage)
      .filter(([key]) =>
        key.startsWith("memory-session-draft:actor-1:project-1:branch-a:"),
      )
      .map(([, value]) => JSON.parse(value)),
  );
  expect(records).toHaveLength(1);
  expect(records[0]?.pending.operation_id).toMatch(/^request-v2:[a-f0-9]{32}$/);
});

test("unknown save survives reload with the exact request, then clears the draft", async ({
  page,
}) => {
  const writes: Record<string, unknown>[] = [];
  let saved: MemorySession | null = null;
  page.on("dialog", (dialog) => dialog.accept());
  await page.route("**/api/**", async (route) => {
    const request = route.request(),
      path = new URL(request.url()).pathname;
    if (request.method() === "POST" && path.endsWith("/sessions")) {
      const body = request.postDataJSON();
      writes.push(body);
      saved = { id: "session-new", revision: 1, document: body.document };
      if (writes.length === 1) return route.abort("failed");
      return route.fulfill({ json: saved });
    }
    if (path.endsWith("/memory"))
      return route.fulfill({
        json: { items: [], sessions: saved ? [saved] : [] },
      });
    return route.fulfill({ json: { items: [] } });
  });
  await page.goto("/tests/fixtures/memory-panel.html");
  await page.getByRole("button", { name: "新建工作会话", exact: true }).click();
  await page.getByLabel("工作会话名称").fill("第二卷规划");
  await page.getByLabel("工作笔记").fill("保留人物高光和主线");
  await page.getByRole("button", { name: "创建工作会话", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "核对并重试上次保存", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("工作笔记")).toBeDisabled();
  await page.reload();
  await expect(page.getByLabel("工作笔记")).toHaveValue("保留人物高光和主线");
  await page
    .getByRole("button", { name: "核对并重试上次保存", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText("工作记录已保存");
  expect(writes).toHaveLength(2);
  expect(writes[1]).toEqual(writes[0]);
  expect(writes[0]?.operation_id).toMatch(/^request-v2:[a-f0-9]{32}$/);
  await expect(page.getByLabel("工作笔记")).toBeEnabled();
  const draftKeys = await page.evaluate(() =>
    Object.keys(localStorage).filter((k) =>
      k.startsWith("memory-session-draft:"),
    ),
  );
  expect(draftKeys).toEqual([]);
});

test("another window cannot erase an unresolved checkpoint request", async ({
  page,
  context,
}) => {
  const writes: Record<string, unknown>[] = [];
  const other = await context.newPage();
  page.on("dialog", (dialog) => dialog.accept());
  await context.route("**/api/**", async (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      writes.push(body);
      if (writes.length === 1) return route.abort("failed");
      return route.fulfill({
        json: { id: "session-safe", revision: 1, document: body.document },
      });
    }
    return route.fulfill({ json: { items: [], sessions: [] } });
  });
  await page.goto("/tests/fixtures/memory-panel.html");
  await other.goto("/tests/fixtures/memory-panel.html");
  await page.getByRole("button", { name: "新建工作会话", exact: true }).click();
  await page.getByLabel("工作会话名称").fill("不能丢的保存");
  await page.getByRole("button", { name: "创建工作会话", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "核对并重试上次保存", exact: true }),
  ).toBeVisible();
  await other
    .getByRole("button", { name: "新建工作会话", exact: true })
    .click();
  await other.getByLabel("工作会话名称").fill("另一个窗口的草稿");
  await page.reload();
  await expect(page.getByLabel("工作会话名称")).toHaveValue("不能丢的保存");
  await page
    .getByRole("button", { name: "核对并重试上次保存", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText("工作记录已保存");
  expect(writes[1]).toEqual(writes[0]);
  const drafts = await other.evaluate(() =>
    Object.entries(localStorage)
      .filter(([key]) => key.startsWith("memory-session-draft:"))
      .map(([, value]) => value),
  );
  expect(drafts.some((value) => value.includes("另一个窗口的草稿"))).toBe(true);
});

test("pending persistence failure prevents first dispatch and remains retryable", async ({
  page,
}) => {
  let writes = 0;
  await page.addInitScript(() => {
    Object.defineProperty(window, "checkpointQuota", {
      value: true,
      writable: true,
    });
    const original = Storage.prototype.setItem;
    Storage.prototype.setItem = function (key, value) {
      if (
        key.startsWith("memory-session-draft:") &&
        value.includes('"pending":{') &&
        Reflect.get(window, "checkpointQuota")
      )
        throw new DOMException("quota", "QuotaExceededError");
      original.call(this, key, value);
    };
  });
  await page.route("**/api/**", async (route) => {
    if (route.request().method() === "POST") {
      writes++;
      return route.fulfill({
        json: {
          id: "session-quota",
          revision: 1,
          document: route.request().postDataJSON().document,
        },
      });
    }
    return route.fulfill({ json: { items: [], sessions: [] } });
  });
  await page.goto("/tests/fixtures/memory-panel.html");
  await page.getByRole("button", { name: "新建工作会话", exact: true }).click();
  await page.getByLabel("工作会话名称").fill("存储不足仍保留输入");
  await page.getByRole("button", { name: "创建工作会话", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("暂存");
  expect(writes).toBe(0);
  await page.evaluate(() => Reflect.set(window, "checkpointQuota", false));
  await page
    .getByRole("button", { name: "核对并重试上次保存", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText("工作记录已保存");
  expect(writes).toBe(1);
});

test("unsaved checkpoint is scoped to its branch and a newer server revision needs explicit selection", async ({
  page,
}) => {
  const saved: MemorySession = {
    id: "session-1",
    revision: 1,
    document: { title: "第一卷", goal: "", notes: "原始记录", focus_ids: [] },
  };
  const writes: Record<string, unknown>[] = [];
  page.on("dialog", (dialog) => dialog.accept());
  await page.route("**/api/**", async (route) => {
    const req = route.request(),
      path = new URL(req.url()).pathname;
    if (req.method() === "POST" && path.endsWith("/sessions")) {
      const body = req.postDataJSON();
      writes.push(body);
      if (writes.length === 1)
        return route.fulfill({
          status: 409,
          json: { error: "工作记录已有新版本" },
        });
      saved.revision = 3;
      saved.document = body.document;
      return route.fulfill({ json: saved });
    }
    if (path.endsWith("/sessions/session-1"))
      return route.fulfill({ json: saved });
    if (path.endsWith("/memory"))
      return route.fulfill({ json: { items: [], sessions: [saved] } });
    return route.fulfill({ json: { items: [] } });
  });
  await page.goto("/tests/fixtures/memory-panel.html");
  await page.getByLabel("当前工作会话").selectOption(saved.id);
  await page.getByLabel("工作笔记").fill("本地未完成的伏笔");
  await page.reload();
  await expect(page.getByLabel("工作笔记")).toHaveValue("本地未完成的伏笔");
  saved.revision = 2;
  saved.document.notes = "另一窗口的最新内容";
  await page.getByRole("button", { name: "保存工作记录", exact: true }).click();
  await page
    .getByRole("button", { name: "读取服务器最新记录", exact: true })
    .click();
  await expect(page.getByTestId("latest-session")).toContainText(
    "另一窗口的最新内容",
  );
  await expect(page.getByLabel("工作笔记")).toHaveValue("本地未完成的伏笔");
  await page
    .getByRole("button", {
      name: "保留我的输入，改用最新版本继续编辑",
      exact: true,
    })
    .click();
  await page.getByRole("button", { name: "保存工作记录", exact: true }).click();
  expect(writes[1]?.expected_revision).toBe(2);
  expect(writes[1]?.operation_id).not.toBe(writes[0]?.operation_id);
  await page.getByLabel("工作笔记").fill("仅属于旧分支的私有草稿");
  await page.getByRole("button", { name: "模拟外部切换", exact: true }).click();
  await expect(page.getByLabel("工作笔记")).toHaveCount(0);
});
