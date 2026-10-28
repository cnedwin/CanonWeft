import { expect, test } from "@playwright/test";

test("回收站按需查看和分页，核对失败时撤下旧期限", async ({ page }) => {
  let unavailable = false;
  const first = {
    id: "1".repeat(32),
    name: "已删除的小说",
    revision: 2,
    window: "WITHIN_WINDOW",
    deleted_at: "2026-10-26T00:00:00Z",
    recover_until: "2026-11-02T00:00:00Z",
    checked_at: "2026-10-26T01:00:00Z",
  };
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/setup")
      return route.fulfill({ json: { required: false } });
    if (url.pathname === "/api/me")
      return route.fulfill({
        json: {
          id: "owner",
          workspace_id: "workspace",
          username: "owner",
          role: "owner",
        },
      });
    if (["/api/projects", "/api/members"].includes(url.pathname))
      return route.fulfill({ json: { items: [] } });
    if (url.pathname === "/api/operations/recycle-bin") {
      if (unavailable)
        return route.fulfill({
          status: 503,
          json: { error: "无法核对删除记录" },
        });
      return route.fulfill({
        json: url.searchParams.has("after")
          ? {
              items: [
                {
                  ...first,
                  id: "2".repeat(32),
                  name: "旧作品",
                  window: "UNVERIFIED_CLOCK",
                  deleted_at: undefined,
                  recover_until: undefined,
                },
              ],
              next_cursor: "",
            }
          : { items: [first], next_cursor: first.id },
      });
    }
    return route.fulfill({ status: 503, json: { error: "测试未启用此功能" } });
  });
  await page.goto("/");
  await page.getByRole("button", { name: "备份保护", exact: true }).click();
  const recycle = page.getByRole("region", { name: "回收站", exact: true });
  await recycle
    .getByRole("button", { name: "查看回收站", exact: true })
    .click();
  await expect(
    recycle.getByText("仍在七天期限内", { exact: false }),
  ).toBeVisible();
  await expect(
    recycle.getByRole("button", { name: "恢复为新副本", exact: true }),
  ).toHaveCount(1);
  await recycle.getByRole("button", { name: "查看更多已删除作品" }).click();
  await expect(
    recycle.getByText("历史删除时间未经确认，无法确定恢复期限", {
      exact: false,
    }),
  ).toBeVisible();
  unavailable = true;
  await recycle.getByRole("button", { name: "刷新回收站" }).click();
  await expect(recycle.getByRole("alert")).toHaveText("无法核对删除记录");
  await expect(recycle.getByText("已删除的小说", { exact: true })).toHaveCount(
    0,
  );
});

test("恢复请求丢失响应后刷新重试复用操作编号，并打开新副本", async ({
  page,
}) => {
  const source = {
    id: "3".repeat(32),
    name: "我的小说",
    revision: 2,
    window: "WITHIN_WINDOW",
    checked_at: "2026-10-26T01:00:00Z",
  };
  const restored = {
    id: "4".repeat(32),
    name: "我的小说（恢复副本）",
    revision: 1,
    role: "owner",
  };
  const requests: {
    operation_id: string;
    revision: number;
    confirm_name: string;
    name: string;
  }[] = [];
  let complete = false;
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/setup")
      return route.fulfill({ json: { required: false } });
    if (url.pathname === "/api/me")
      return route.fulfill({
        json: {
          id: "owner",
          workspace_id: "workspace",
          username: "owner",
          role: "owner",
        },
      });
    if (url.pathname === "/api/members")
      return route.fulfill({ json: { items: [] } });
    if (url.pathname === "/api/projects")
      return route.fulfill({ json: { items: complete ? [restored] : [] } });
    if (url.pathname === "/api/operations/recycle-bin")
      return route.fulfill({ json: { items: [source], next_cursor: "" } });
    if (url.pathname === `/api/projects/${source.id}/recover`) {
      requests.push(route.request().postDataJSON());
      if (requests.length === 1) return route.abort("failed");
      complete = true;
      return route.fulfill({ status: 201, json: restored });
    }
    return route.fulfill({ status: 503, json: { error: "测试未启用此功能" } });
  });
  const open = async () => {
    await page.getByRole("button", { name: "备份保护", exact: true }).click();
    const recycle = page.getByRole("region", { name: "回收站", exact: true });
    await recycle
      .getByRole("button", { name: "查看回收站", exact: true })
      .click();
    await recycle
      .getByRole("button", { name: "恢复为新副本", exact: true })
      .click();
    return recycle;
  };
  await page.goto("/");
  let recycle = await open();
  await expect(recycle.getByLabel("恢复副本名称")).toHaveValue(restored.name);
  await recycle
    .getByRole("button", { name: "确认恢复为新副本", exact: true })
    .click();
  await expect(recycle.getByRole("alert")).toBeVisible();
  await page.reload();
  recycle = await open();
  await expect(recycle.getByLabel("恢复副本名称")).toHaveValue(restored.name);
  await recycle
    .getByRole("button", { name: "确认恢复为新副本", exact: true })
    .click();
  await expect(recycle.getByRole("status")).toContainText("已恢复为");
  expect(requests).toHaveLength(2);
  expect(requests[0]).toEqual(requests[1]);
  expect(requests[1].revision).toBe(source.revision);
  expect(requests[1].confirm_name).toBe(source.name);
  await recycle.getByRole("button", { name: "返回工作室使用副本" }).click();
  await expect(page.getByRole("button", { name: "打开设定库" })).toBeVisible();
});
