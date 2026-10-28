import { expect, test, type Page } from "@playwright/test";

const project = "1".repeat(32);
const job = {
  id: "2".repeat(32),
  project_id: project,
  state: "PLANNED",
  plan_hash: "a".repeat(64),
  inventory_revision: 3,
  checkpoint_current: true,
  restored_disabled: false,
  expiring: 4,
  removable_objects: 2,
  blocked: 1,
  overdue: false,
  cutoff: "2026-10-26T01:00:00Z",
  created_at: "2026-10-26T01:00:01Z",
  updated_at: "2026-10-26T01:00:01Z",
};

async function openProtection(page: Page) {
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
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
    if (path === "/api/projects")
      return route.fulfill({
        json: {
          items: [
            { id: project, name: "测试小说", revision: 1, role: "owner" },
          ],
        },
      });
    if (path === "/api/members") return route.fulfill({ json: { items: [] } });
    if (path === "/api/operations/retention-execution")
      return route.fulfill({ json: { enabled: false } });
    return route.fulfill({
      status: 503,
      json: { error: "此测试仅覆盖清理预览" },
    });
  });
}

test("周期清理配置及自动任务来源可见，浏览不启动清理", async ({ page }) => {
  await openProtection(page);
  await page.route("**/api/operations/retention-execution", (route) =>
    route.fulfill({
      json: {
        enabled: true,
        automatic_configured: true,
        automatic_enabled: true,
      },
    }),
  );
  await page.route(`**/api/projects/${project}/retention-jobs**`, (route) => {
    expect(route.request().method()).toBe("GET");
    return route.fulfill({
      json: {
        items: [{ ...job, automatic: true, state: "COMPLETE" }],
        next_cursor: "",
      },
    });
  });
  await page.goto("/");
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  await page.getByLabel("清理预览的作品").selectOption(project);
  await expect(
    page.getByText("来自周期自动清理", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText(/自动清理：已启用，每天生成一批/)).toBeVisible();
  await expect(
    page.getByRole("button", { name: "执行此清理计划", exact: true }),
  ).toHaveCount(0);
});

test("清理预览可从持久列表重新打开、分页并取消，过期状态如实显示", async ({
  page,
}, testInfo) => {
  await openProtection(page);
  let cancelled = false;
  const older = {
    ...job,
    id: "3".repeat(32),
    checkpoint_current: false,
    restored_disabled: true,
  };
  await page.route(
    `**/api/projects/${project}/retention-jobs**`,
    async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname.endsWith("/cancel")) {
        expect(route.request().postDataJSON()).toEqual({
          plan_hash: job.plan_hash,
        });
        cancelled = true;
        return route.fulfill({ json: { ...job, state: "CANCELLED" } });
      }
      expect(route.request().method()).toBe("GET");
      return route.fulfill({
        json: url.searchParams.has("after")
          ? { items: [older], next_cursor: "" }
          : {
              items: [{ ...job, state: cancelled ? "CANCELLED" : "PLANNED" }],
              next_cursor: job.id,
            },
      });
    },
  );
  await page.goto("/");
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  await page.getByLabel("清理预览的作品").selectOption(project);
  await expect(
    page.getByText("待清理引用：4；文件：2；暂缓项：1", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "查看更早的预览", exact: true })
    .click();
  await expect(
    page.getByText("资料已变化，需要重新生成预览", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("恢复副本中的历史计划，仅供查看", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "取消此预览", exact: true }),
  ).toHaveCount(1);
  await page.screenshot({
    path: testInfo.outputPath("retention-plans.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "取消此预览", exact: true }).click();
  await expect(page.getByText("已取消", { exact: true })).toBeVisible();
});

test("确认后执行原计划并刷新结果，恢复历史不允许执行", async ({ page }) => {
  await openProtection(page);
  await page.route("**/api/operations/retention-execution", (route) =>
    route.fulfill({ json: { enabled: true } }),
  );
  let starts = 0,
    state = "PLANNED";
  await page.route(
    `**/api/projects/${project}/retention-jobs**`,
    async (route) => {
      if (route.request().method() === "POST") {
        expect(route.request().url()).toContain(`/${job.id}/start`);
        expect(route.request().postDataJSON()).toEqual({
          plan_hash: job.plan_hash,
        });
        starts++;
        state = "DETACHING";
        return route.fulfill({ status: 202, json: { ...job, state } });
      }
      return route.fulfill({
        json: {
          items: [
            { ...job, state },
            { ...job, id: "3".repeat(32), restored_disabled: true },
          ],
          next_cursor: "",
        },
      });
    },
  );
  await page.goto("/");
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  await page.getByLabel("清理预览的作品").selectOption(project);
  await expect(
    page.getByRole("button", { name: "执行此清理计划", exact: true }),
  ).toHaveCount(1);
  await page
    .getByRole("button", { name: "执行此清理计划", exact: true })
    .click();
  expect(starts).toBe(0);
  await page.getByRole("button", { name: "确认清理", exact: true }).click();
  await expect(
    page.getByText("正在清理到期材料", { exact: true }),
  ).toBeVisible();
  state = "COMPLETE";
  await expect(page.getByText("清理已完成", { exact: true })).toBeVisible({
    timeout: 10000,
  });
  expect(starts).toBe(1);
  await expect(
    page.getByRole("button", { name: "执行此清理计划", exact: true }),
  ).toHaveCount(0);
});

test("本机请求暂存失败时不发送创建请求", async ({ page }) => {
  await openProtection(page);
  await page.addInitScript(() => {
    const original = Storage.prototype.setItem;
    Storage.prototype.setItem = function (key: string, value: string) {
      if (key.startsWith("cleanup-preview-v1:"))
        throw new Error("本机暂存空间不可用");
      return original.call(this, key, value);
    };
  });
  let creates = 0;
  await page.route(
    `**/api/projects/${project}/retention-jobs**`,
    async (route) => {
      if (route.request().method() === "POST") creates++;
      return route.fulfill({ json: { items: [], next_cursor: "" } });
    },
  );
  await page.goto("/");
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  await page.getByLabel("清理预览的作品").selectOption(project);
  await page
    .getByRole("button", { name: "生成并保存清理预览", exact: true })
    .click();
  await expect(
    page.getByText("本机暂存空间不可用", { exact: true }),
  ).toBeVisible();
  expect(creates).toBe(0);
});

test("保存回执丢失后刷新仍使用原操作编号重试", async ({ page }) => {
  await openProtection(page);
  const operations: string[] = [];
  await page.route(
    `**/api/projects/${project}/retention-jobs**`,
    async (route) => {
      if (route.request().method() === "GET")
        return route.fulfill({ json: { items: [], next_cursor: "" } });
      operations.push(route.request().postDataJSON().operation_id);
      if (operations.length === 1) return route.abort();
      return route.fulfill({ json: job });
    },
  );
  await page.goto("/");
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  await page.getByLabel("清理预览的作品").selectOption(project);
  await page
    .getByRole("button", { name: "生成并保存清理预览", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "重试保存原预览", exact: true }),
  ).toBeEnabled();
  await page.reload();
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  await page.getByLabel("清理预览的作品").selectOption(project);
  await page
    .getByRole("button", { name: "重试保存原预览", exact: true })
    .click();
  expect(operations).toHaveLength(2);
  expect(operations[0]).toMatch(/^request-v2:[a-f0-9]{32}$/);
  expect(operations[1]).toBe(operations[0]);
  await expect(
    page.getByText("待清理引用：4；文件：2；暂缓项：1", { exact: true }),
  ).toBeVisible();
});
