import { expect, test, type Page, type TestInfo } from "@playwright/test";

// This isolated source fixture has no login, persisted manuscript or model call.
const fixtureHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8"></head>
<body><div id="app"></div><script type="module">
import { createApp, h } from "/node_modules/.vite/deps/vue.js";
import GenerationPanel from "/src/features/ip/GenerationPanel.vue";
createApp({ render: () => h(GenerationPanel, {
  project: { id: "project", name: "隔离候选验证", role: "owner", revision: 1 },
  actor: { id: "owner", workspace_id: "workspace", username: "owner", role: "owner" },
  branch: { id: "branch", head: "", snapshot: { entities: [], assertions: [] } },
  writing: { root: "/novels/book/chapters/body", revision: 1, hash: "draft-hash" }
}) }).mount("#app");
</script></body></html>`;

type Ordering = "late-success" | "late-error" | "new-error";

async function fixture(page: Page, info: TestInfo, ordering: Ordering) {
  const source = await (
    await page.request.get("/src/features/ip/GenerationPanel.vue")
  ).text();
  const vue = source.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(
    vue,
    "use the component's exact Vite Vue module identity",
  ).toBeTruthy();
  const html = fixtureHTML.replace("/node_modules/.vite/deps/vue.js", vue!);
  await info.attach("generation-lifecycle-fixture.html", {
    body: Buffer.from(html),
    contentType: "text/html",
  });
  let task = {
    id: "task-111111",
    state: "PREPARED",
    paused: false,
    cancelled: false,
    frozen: {
      role: "writer",
      config: { name: "本地协议替身", model: "test-only", revision: 1 },
      price: { currency: "CNY" },
      estimate_micros: 1000000,
    },
    call: { state: "RESERVED", charged_micros: 0, reserved_micros: 1000000 },
  };
  let listReads = 0;
  let entered!: () => void;
  const seen = new Promise<void>((resolve) => {
    entered = resolve;
  });
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const controls: string[] = [];
  const root = "/api/projects/project/generations";
  await page.route("**/tests/fixtures/generation-lifecycle.html", (route) =>
    route.fulfill({ contentType: "text/html", body: html }),
  );
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/generation-options"))
      return route.fulfill({
        json: {
          items: [
            {
              role: "writer",
              name: "本地协议替身",
              model: "test-only",
              price: { id: "price", currency: "CNY" },
            },
          ],
        },
      });
    if (path.endsWith("/capabilities"))
      return route.fulfill({ json: { items: [] } });
    if (path === root) {
      const result = structuredClone(task);
      const read = ++listReads;
      if (read === 2) {
        entered();
        await held;
        if (ordering === "late-error")
          return route.fulfill({ status: 403, json: { error: "旧列表拒绝" } });
      }
      if (read === 3 && ordering === "new-error")
        return route.fulfill({
          status: 403,
          json: { error: "当前列表权限已撤销" },
        });
      return route.fulfill({ json: { items: [result], runtime_ready: true } });
    }
    if (path === `${root}/${task.id}/control`) {
      const action = request.postDataJSON().action;
      controls.push(action);
      task = {
        ...task,
        paused: action === "pause",
        cancelled: action === "cancel",
        state: action === "cancel" ? "CANCELLED" : task.state,
      };
      return route.fulfill({ json: task });
    }
    if (path === `${root}/${task.id}`) return route.fulfill({ json: task });
    return route.fulfill({
      status: 404,
      json: { error: `未预期接口 ${path}` },
    });
  });
  const now = new Date("2026-10-28T00:00:00Z");
  await page.clock.install({ time: now });
  await page.clock.pauseAt(now);
  await page.goto("/tests/fixtures/generation-lifecycle.html");
  await page
    .getByRole("button", { name: "查看任务 task-1", exact: true })
    .click();
  await expect(page.locator(".generation-detail")).toContainText("任务详情");
  await page
    .getByLabel("希望 AI 协助的内容", { exact: true })
    .fill("保留作者的写作要求");
  await page.clock.fastForward(2500);
  await seen;
  return { controls, release, reads: () => listReads };
}

async function releaseList(page: Page, release: () => void) {
  const response = page.waitForResponse((value) =>
    new URL(value.url()).pathname.endsWith("/generations"),
  );
  release();
  await (await response).finished();
  await page.clock.runFor(100);
}

for (const action of ["pause", "cancel"] as const) {
  test(`已确认${action === "pause" ? "暂停" : "取消"}后旧轮询列表不能恢复旧任务按钮`, async ({
    page,
  }, info) => {
    const state = await fixture(page, info, "late-success");
    await page.clock.fastForward(10000);
    expect(state.reads()).toBe(2);
    await page
      .getByRole("button", {
        name: action === "pause" ? "暂停任务" : "取消任务",
        exact: true,
      })
      .click();
    const card = page.locator(".generation-task");
    const status = action === "pause" ? "已暂停" : "已取消";
    await expect(card).toContainText(status);
    await expect(
      page.getByRole("button", { name: "查看任务 task-1", exact: true }),
    ).toBeEnabled();
    await releaseList(page, state.release);
    await expect(card).toContainText(status);
    if (action === "pause") {
      await expect(
        page.getByRole("button", { name: "恢复任务", exact: true }),
      ).toBeVisible();
      await page.getByRole("button", { name: "恢复任务", exact: true }).click();
      await expect(card).not.toContainText("已暂停");
      expect(state.controls).toEqual(["pause", "resume"]);
    } else {
      await expect(
        page.getByRole("button", { name: "取消任务", exact: true }),
      ).toHaveCount(0);
      await expect(
        page.getByRole("button", {
          name: "执行此请求（需要开启付费预算）",
          exact: true,
        }),
      ).toHaveCount(0);
      expect(state.controls).toEqual(["cancel"]);
    }
    await expect(
      page.getByLabel("希望 AI 协助的内容", { exact: true }),
    ).toHaveValue("保留作者的写作要求");
    await expect(page.getByRole("alert")).toHaveCount(0);
    const reads = state.reads();
    await page.clock.fastForward(2500);
    await expect.poll(state.reads).toBe(reads + 1);
    await expect(card).toContainText(
      action === "pause" ? "已准备，未发送" : "已取消",
    );
  });
}

test("旧列表错误不能清空控制后的新列表和详情", async ({ page }, info) => {
  const state = await fixture(page, info, "late-error");
  await page.getByRole("button", { name: "取消任务", exact: true }).click();
  await expect(page.locator(".generation-task")).toContainText("已取消");
  await expect(page.locator(".generation-detail")).toContainText("已取消");
  await releaseList(page, state.release);
  await expect(page.locator(".generation-task")).toContainText("已取消");
  await expect(page.locator(".generation-detail")).toContainText("已取消");
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("当前列表权限拒绝后旧成功回包不能恢复任务", async ({ page }, info) => {
  const state = await fixture(page, info, "new-error");
  await page.getByRole("button", { name: "取消任务", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("当前列表权限已撤销");
  await expect(page.locator(".generation-task")).toHaveCount(0);
  await releaseList(page, state.release);
  await expect(page.locator(".generation-task")).toHaveCount(0);
  await expect(page.locator(".generation-detail")).toHaveCount(0);
  await expect(page.getByRole("alert")).toContainText("当前列表权限已撤销");
});
