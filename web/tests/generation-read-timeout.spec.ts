import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { createServer } from "node:http";
import { once } from "node:events";
import type { AddressInfo } from "node:net";

// Real loopback HTTP fixtures; no login or real provider calls.
const readTimeoutMs = 15000;
const root = "/api/projects/project/generations";

type ReadMode =
  | "list"
  | "detail"
  | "body"
  | "dispose"
  | "options"
  | "preview"
  | "scope";

interface Denial {
  status: 401 | 403;
  body: "hang" | "invalid";
  superseded?: boolean;
}

async function fixture(
  page: Page,
  info: TestInfo,
  mode: ReadMode,
  denial?: Denial,
) {
  const source = await (
    await page.request.get("/src/features/ip/GenerationPanel.vue")
  ).text();
  const vue = source.match(/from\s+["']([^"']*\/vue\.js[^"']*)["']/)?.[1];
  expect(vue).toBeTruthy();
  const vite = new URL(info.project.use.baseURL!).origin;
  const html = `<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8"></head>
<body><button id="leave">离开隔离面板</button><button id="scope">切换隔离作品</button><div id="app"></div><script type="module">
import { createApp, h, ref } from ${JSON.stringify(new URL(vue!, vite).href)};
import GenerationPanel from ${JSON.stringify(`${vite}/src/features/ip/GenerationPanel.vue`)};
const changed = ref(false);
const app = createApp({ render: () => h(GenerationPanel, {
  key: changed.value ? "next" : "original",
  project: { id: changed.value ? "project-next" : "project", name: "隔离读取验证", role: "owner", revision: 1 },
  actor: { id: "owner", workspace_id: "workspace", username: "owner", role: "owner" },
  branch: { id: changed.value ? "branch-next" : "branch", head: "", snapshot: { entities: [], assertions: [] } },
  writing: { root: "/novels/book/chapters/body", revision: 1, hash: "draft-hash" }
}) });
app.mount("#app");
document.getElementById("leave").addEventListener("click", () => app.unmount());
document.getElementById("scope").addEventListener("click", () => { changed.value = true; });
</script></body></html>`;
  await info.attach("read-timeout-fixture.html", {
    body: Buffer.from(html),
    contentType: "text/html",
  });
  const counts = {
    lists: 0,
    details: 0,
    options: 0,
    nextLists: 0,
    hangs: 0,
    aborted: 0,
    writes: [] as { method: string | undefined; path: string }[],
  };
  let entered!: () => void;
  const pending = new Promise<void>((resolve) => {
    entered = resolve;
  });
  let recovering = false;
  const task = {
    id: "task-111111",
    state: mode === "preview" ? "SUCCEEDED" : "RUNNING",
    paused: false,
    cancelled: false,
    frozen: {
      role: "writer",
      config: { name: "隔离本地替身", model: "test-only", revision: 1 },
      price: { currency: "CNY" },
      estimate_micros: 1000000,
    },
  };
  const server = createServer((request, response) => {
    const path = new URL(request.url, "http://127.0.0.1").pathname;
    if (request.method !== "GET") {
      counts.writes.push({ method: request.method, path });
      if (
        denial?.superseded &&
        request.method === "POST" &&
        path === `${root}/${task.id}/control`
      ) {
        let body = "";
        request.on("data", (chunk) => {
          body += chunk;
        });
        request.on("end", () => {
          if (JSON.parse(body).action !== "cancel") {
            response.writeHead(400).end();
            return;
          }
          task.state = "CANCELLED";
          task.cancelled = true;
          response
            .writeHead(200, { "Content-Type": "application/json" })
            .end(JSON.stringify(task));
        });
        return;
      }
      response
        .writeHead(405, { "Content-Type": "application/json" })
        .end('{"error":"fixture forbids writes"}');
      return;
    }
    if (path === "/") {
      response
        .writeHead(200, { "Content-Type": "text/html; charset=utf-8" })
        .end(html);
      return;
    }
    const json = (value) =>
      response
        .writeHead(200, { "Content-Type": "application/json" })
        .end(JSON.stringify(value));
    if (path.endsWith("/generation-options")) {
      counts.options++;
      if (mode === "options" && counts.options === 1) {
        counts.hangs++;
        response.on("close", () => {
          counts.aborted++;
        });
        entered();
        return;
      }
      json({
        items: [
          {
            role: "writer",
            name: "隔离本地替身",
            model: "test-only",
            price: { id: "price", currency: "CNY" },
          },
        ],
      });
      return;
    }
    if (path.endsWith("/capabilities")) {
      json({ items: [] });
      return;
    }
    if (path === "/api/projects/project-next/generations") {
      counts.nextLists++;
      json({ items: [{ ...task, id: "task-222222" }], runtime_ready: true });
      return;
    }
    if (path === "/api/projects/project-next/generations/task-222222") {
      json({ ...task, id: "task-222222" });
      return;
    }
    if (path === "/api/novels/book/chapters/body/writer/task-111111") {
      counts.hangs++;
      response.on("close", () => {
        counts.aborted++;
      });
      entered();
      return;
    }
    if (path === root) {
      counts.lists++;
      if (denial && counts.lists === 2) {
        response.writeHead(denial.status, {
          "Content-Type": "application/json",
        });
        if (denial.body === "hang") {
          counts.hangs++;
          response.on("close", () => {
            counts.aborted++;
          });
          response.write('{"error":');
        } else response.end('{"error":');
        entered();
        return;
      }
      if ((mode === "list" || mode === "body") && counts.lists === 2) {
        counts.hangs++;
        response.on("close", () => {
          counts.aborted++;
        });
        if (mode === "body") {
          response.writeHead(200, { "Content-Type": "application/json" });
          response.write('{"items":[');
        }
        entered();
        return;
      }
      json({ items: [task], runtime_ready: true });
      return;
    }
    if (path === `${root}/${task.id}`) {
      counts.details++;
      if (
        ["detail", "dispose", "scope"].includes(mode) &&
        counts.details === 2
      ) {
        counts.hangs++;
        response.on("close", () => {
          counts.aborted++;
        });
        entered();
        return;
      }
      const complete = recovering;
      json({
        ...task,
        state: complete ? "SUCCEEDED" : task.state,
        call: {
          state: complete ? "SUCCEEDED" : "DISPATCHED",
          charged_micros: complete ? 250000 : 0,
          reserved_micros: 1000000,
        },
        ...(complete || mode === "preview"
          ? {
              output: {
                text: "恢复读取后的候选",
                input_tokens: 10,
                output_tokens: 10,
              },
            }
          : {
              preview: {
                text: "先前读取的私人候选",
                sequence: 1,
                hash: "preview-hash",
                provisional: true,
              },
            }),
      });
      return;
    }
    response
      .writeHead(404, { "Content-Type": "application/json" })
      .end(JSON.stringify({ error: `Unexpected fixture path ${path}` }));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const now = new Date("2026-10-28T00:00:00Z");
  await page.clock.install({ time: now });
  await page.clock.pauseAt(now);
  await page.goto(
    `http://127.0.0.1:${(server.address() as AddressInfo).port}/`,
  );
  const state = {
    counts,
    recover: () => {
      recovering = true;
    },
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
  if (mode === "options") {
    await pending;
    return state;
  }
  await page
    .getByRole("button", { name: "查看任务 task-1", exact: true })
    .click();
  await expect(page.locator(".generation-detail")).toContainText("0.000000");
  await page
    .getByLabel("希望 AI 协助的内容", { exact: true })
    .fill("不丢失作者未保存的写作要求");
  if (mode === "preview")
    await page
      .getByRole("button", { name: "查看正文候选", exact: true })
      .click();
  else await page.clock.fastForward(2500);
  await pending;
  return state;
}

for (const mode of ["list", "detail", "body"] as const) {
  test(`状态GET ${mode}永久挂起应有界取消并恢复轮询且不重发写请求`, async ({
    page,
  }, info) => {
    const state = await fixture(page, info, mode);
    try {
      await page.clock.fastForward(readTimeoutMs);
      state.recover();
      await info.attach("read-timeout-state-before-recovery.json", {
        body: Buffer.from(JSON.stringify(state.counts, null, 2)),
        contentType: "application/json",
      });
      expect(state.counts.writes).toEqual([]);
      await expect.poll(() => state.counts.aborted).toBe(1);
      await page.clock.fastForward(2500);
      await expect.poll(() => state.counts.lists).toBeGreaterThanOrEqual(3);
      await expect(page.locator(".generation-detail")).toContainText(
        "0.250000",
      );
      await expect(
        page.getByLabel("希望 AI 协助的内容", { exact: true }),
      ).toHaveValue("不丢失作者未保存的写作要求");
      expect(state.counts.writes).toEqual([]);
    } finally {
      await state.close();
    }
  });
}

test("卸载面板应取消本组件挂起GET并停止轮询，不取消后台任务", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "dispose");
  try {
    await page
      .getByRole("button", { name: "离开隔离面板", exact: true })
      .click();
    await expect(page.locator(".generation-panel")).toHaveCount(0);
    await expect.poll(() => state.counts.aborted).toBe(1);
    const before = { lists: state.counts.lists, details: state.counts.details };
    await page.clock.fastForward(readTimeoutMs + 2500);
    expect({
      lists: state.counts.lists,
      details: state.counts.details,
    }).toEqual(before);
    expect(state.counts.writes).toEqual([]);
  } finally {
    await state.close();
  }
});

test("初始化options超时仍应启动只读poll且不出现付费准备入口", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "options");
  try {
    await page.clock.fastForward(readTimeoutMs);
    await expect.poll(() => state.counts.aborted).toBe(1);
    await expect.poll(() => state.counts.lists).toBeGreaterThanOrEqual(1);
    await expect(
      page.getByRole("button", { name: "保存请求并估算费用", exact: true }),
    ).toHaveCount(0);
    await expect(page.locator(".generation-task")).toContainText("请求已发送");
    expect(state.counts.writes).toEqual([]);
  } finally {
    await state.close();
  }
});

test("手动候选预览拥有独立60秒上限并最终释放busy，不沿用15秒状态读", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "preview");
  try {
    const button = page.getByRole("button", {
      name: "查看正文候选",
      exact: true,
    });
    await page.clock.fastForward(readTimeoutMs);
    expect(state.counts.aborted).toBe(0);
    await expect(button).toBeDisabled();
    await page.clock.fastForward(60000 - readTimeoutMs);
    await expect.poll(() => state.counts.aborted).toBe(1);
    await expect(button).toBeEnabled();
    await expect(
      page.getByLabel("希望 AI 协助的内容", { exact: true }),
    ).toHaveValue("不丢失作者未保存的写作要求");
    await expect(page.locator(".generation-task")).toContainText("候选已生成");
    expect(state.counts.writes).toEqual([]);
  } finally {
    await state.close();
  }
});

test("切换keyed作品scope取消旧GET，旧中止错误不得进入新面板", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "scope");
  try {
    await page
      .getByRole("button", { name: "切换隔离作品", exact: true })
      .click();
    await expect.poll(() => state.counts.aborted).toBe(1);
    await expect(
      page.getByRole("button", { name: "查看任务 task-2", exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
    await page.clock.fastForward(readTimeoutMs + 2500);
    await expect(page.getByRole("alert")).toHaveCount(0);
    expect(state.counts.writes).toEqual([]);
  } finally {
    await state.close();
  }
});

for (const status of [401, 403] as const) {
  for (const body of ["hang", "invalid"] as const) {
    test(`已收到HTTP ${status} ${body}响应体时仍须清除当前私有快照`, async ({
      page,
    }, info) => {
      const state = await fixture(page, info, "list", { status, body });
      try {
        if (body === "hang") {
          await page.clock.fastForward(readTimeoutMs);
          await expect.poll(() => state.counts.aborted).toBe(1);
        }
        await expect(page.locator(".generation-task")).toHaveCount(0);
        await expect(page.locator(".generation-detail")).toHaveCount(0);
        await expect(page.getByRole("alert")).not.toContainText("读取超时");
        await expect(
          page.getByLabel("希望 AI 协助的内容", { exact: true }),
        ).toHaveValue("不丢失作者未保存的写作要求");
        expect(state.counts.writes).toEqual([]);
      } finally {
        await state.close();
      }
    });
  }
}

test("旧epoch HTTP 403挂起响应体超时不得清除控制后的新快照", async ({
  page,
}, info) => {
  const state = await fixture(page, info, "list", {
    status: 403,
    body: "hang",
    superseded: true,
  });
  try {
    await page.getByRole("button", { name: "取消任务", exact: true }).click();
    await expect(page.locator(".generation-task")).toContainText("已取消");
    await expect(page.locator(".generation-detail")).toContainText("已取消");
    await page.clock.fastForward(readTimeoutMs);
    await expect.poll(() => state.counts.aborted).toBe(1);
    await expect(page.locator(".generation-task")).toContainText("已取消");
    await expect(page.locator(".generation-detail")).toContainText("已取消");
    await expect(page.getByRole("alert")).toHaveCount(0);
    expect(state.counts.writes).toEqual([
      { method: "POST", path: `${root}/task-111111/control` },
    ]);
  } finally {
    await state.close();
  }
});
