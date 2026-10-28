import { expect, test, type Page } from "@playwright/test";

async function fixture(
  page: Page,
  late: "poll" | "control" | "none" = "poll",
  extraBatch = false,
) {
  let run = {
    id: "batch-111111",
    branch_id: "branch",
    state: "RUNNING",
    revision: 1,
    paused: false,
    reason: "",
    currency: "CNY",
    budget_limit_micros: 1000000,
    spent_micros: 0,
    uncertain_calls: 0,
    frozen: {
      input: { book_id: "book" },
      definition: {
        definition: { key: "novel-batch-fixture", steps: [{ key: "chapter" }] },
      },
    },
    steps: [],
  };
  let other = {
    ...structuredClone(run),
    id: "batch-222222",
    spent_micros: 500000,
  };
  const controls: { action: string; expected_revision: number }[] = [];
  let detailReads = 0;
  let release: (() => void) | undefined;
  let entered: (() => void) | undefined;
  const pollEntered = new Promise<void>((resolve) => {
    entered = resolve;
  });
  let controlled: (() => void) | undefined;
  const controlEntered = new Promise<void>((resolve) => {
    controlled = resolve;
  });
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const root = "/api/projects/project/recipe-executions";
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/writing-batches/chapters"))
      return route.fulfill({ json: { items: [] } });
    if (path.endsWith("/generation-options"))
      return route.fulfill({ json: { items: [] } });
    if (path === root)
      return route.fulfill({
        json: { items: extraBatch ? [run, other] : [run] },
      });
    if (extraBatch && path === `${root}/${other.id}`)
      return route.fulfill({ json: other });
    if (path === `${root}/${run.id}`) {
      if (++detailReads === 2) {
        entered?.();
        if (late === "poll") {
          const result = structuredClone(run);
          await held;
          return route.fulfill({ json: result });
        }
        if (late === "control") {
          await controlEntered;
          run = { ...run, spent_micros: 250000 };
        }
      }
      return route.fulfill({ json: run });
    }
    if (path === `${root}/${run.id}/control`) {
      const input = request.postDataJSON();
      controls.push(input);
      if (input.expected_revision !== run.revision)
        return route.fulfill({
          status: 409,
          json: { error: "批次版本已变化" },
        });
      run = {
        ...run,
        revision: run.revision + 1,
        paused: input.action === "pause",
      };
      const result = structuredClone(run);
      if (late === "control" && input.action === "pause") {
        controlled?.();
        await held;
      }
      return route.fulfill({ json: result });
    }
    return route.fulfill({ status: 404, json: { error: path } });
  });
  const now = new Date("2026-10-28T00:00:00Z");
  await page.clock.install({ time: now });
  await page.clock.pauseAt(now);
  await page.goto("/tests/fixtures/author-workbench.html?batch");
  await page.getByRole("button", { name: /1章 · 执行中 · 111111/ }).click();
  await expect(
    page.getByRole("button", { name: "暂停任务", exact: true }),
  ).toBeEnabled();
  return {
    controls,
    pollEntered,
    release: () => release?.(),
    settle: (amount: number, second = false) => {
      if (second) other = { ...other, spent_micros: amount };
      else run = { ...run, spent_micros: amount };
    },
  };
}

test("迟到的批次轮询不能撤回已确认暂停或重用旧控制版本", async ({ page }) => {
  const state = await fixture(page);
  const poll = page.waitForRequest((request) =>
    request.url().endsWith("/recipe-executions/batch-111111"),
  );
  await page.clock.fastForward(2500);
  await poll;
  await state.pollEntered;
  await page.getByRole("button", { name: "暂停任务", exact: true }).click();
  const progress = page.getByRole("region", { name: "连续章节进度" });
  await expect(progress).toContainText("已暂停");
  const stale = page.waitForResponse((response) =>
    response.url().endsWith("/recipe-executions/batch-111111"),
  );
  state.release();
  await (await stale).finished();
  await page.clock.runFor(100);
  await expect(progress).toContainText("已暂停");
  await page.getByRole("button", { name: "恢复任务", exact: true }).click();
  await expect(progress).not.toContainText("已暂停");
  expect(state.controls).toEqual([
    { action: "pause", expected_revision: 1 },
    { action: "resume", expected_revision: 2 },
  ]);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("迟到的控制回执不能覆盖同版本的新费用快照", async ({ page }) => {
  const state = await fixture(page, "control");
  await page.clock.fastForward(2500);
  await state.pollEntered;
  await page.getByRole("button", { name: "暂停任务", exact: true }).click();
  const progress = page.getByRole("region", { name: "连续章节进度" });
  await expect(progress).toContainText("已记费用 0.25");
  const stale = page.waitForResponse((response) =>
    response.url().endsWith("/recipe-executions/batch-111111/control"),
  );
  state.release();
  await (await stale).finished();
  await page.clock.runFor(100);
  await expect(progress).toContainText("已记费用 0.25");
  await page.getByRole("button", { name: "恢复任务", exact: true }).click();
  expect(state.controls).toEqual([
    { action: "pause", expected_revision: 1 },
    { action: "resume", expected_revision: 2 },
  ]);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("同版本后续刷新读取实际结算及对账调整，不锁死或取费用最大值", async ({
  page,
}) => {
  const state = await fixture(page, "none");
  const progress = page.getByRole("region", { name: "连续章节进度" });
  state.settle(250000);
  await page.getByRole("button", { name: "刷新记录", exact: true }).click();
  await expect(progress).toContainText("已记费用 0.25");
  state.settle(100000);
  await page.getByRole("button", { name: "刷新记录", exact: true }).click();
  await expect(progress).toContainText("已记费用 0.1 / 上限");
  await page.getByRole("button", { name: "暂停任务", exact: true }).click();
  await expect(progress).toContainText("已暂停");
  expect(state.controls).toEqual([{ action: "pause", expected_revision: 1 }]);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("切换批次后旧回包不覆盖目标，目标同版本刷新与重新选择仍可用", async ({
  page,
}) => {
  const state = await fixture(page, "poll", true);
  await page.clock.fastForward(2500);
  await state.pollEntered;
  await page.getByRole("button", { name: /222222$/ }).click();
  const progress = page.getByRole("region", { name: "连续章节进度" });
  await expect(progress).toContainText("已记费用 0.5");
  const stale = page.waitForResponse((response) =>
    response.url().endsWith("/recipe-executions/batch-111111"),
  );
  state.release();
  await (await stale).finished();
  await page.clock.runFor(100);
  await expect(progress).toContainText("已记费用 0.5");
  state.settle(100000, true);
  await page.getByRole("button", { name: "刷新记录", exact: true }).click();
  await expect(progress).toContainText("已记费用 0.1 / 上限");
  await page.getByRole("button", { name: /111111$/ }).click();
  await expect(progress).toContainText("已记费用 0 / 上限");
  await page.getByRole("button", { name: "暂停任务", exact: true }).click();
  await expect(progress).toContainText("已暂停");
  expect(state.controls).toEqual([{ action: "pause", expected_revision: 1 }]);
  await expect(page.getByRole("alert")).toHaveCount(0);
});
