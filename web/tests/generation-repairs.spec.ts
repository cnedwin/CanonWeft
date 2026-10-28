import { test, expect, type Page } from "@playwright/test";
import { loginAsOwner } from "./owner-session";

// Real app/project/branch; model output and repair endpoints are controlled UI
// fixtures. The separate MySQL test proves actual provenance and preparation.
async function openRepair(page: Page) {
  await page.clock.install();
  await page.clock.pauseAt(new Date(Date.now() + 1000));
  await loginAsOwner(page);
  await page
    .getByLabel("项目名称", { exact: true })
    .fill(`局部修订 ${Date.now()}`);
  const response = page.waitForResponse(
    (r) => r.url().endsWith("/api/projects") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  const project = await (await response).json();
  const root = `/api/projects/${project.id}`;
  const task = {
    id: "a".repeat(32),
    state: "SUCCEEDED",
    cancelled: false,
    paused: false,
    frozen: {
      role: "continuity_editor",
      change_plan_id: "c".repeat(32),
      base_head: "",
      context_id: "",
      estimate_micros: 10,
      config: { name: "本地界面夹具", model: "fixture", revision: 1 },
      price: { currency: "USD" },
    },
    output: { text: "结构化局部修订候选", input_tokens: 10, output_tokens: 10 },
  };
  let denied = false;
  await page.route(`**${root}/generation-options`, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            role: "continuity_editor",
            name: "本地夹具",
            model: "fixture",
            price: { id: "price", currency: "USD" },
          },
        ],
      },
    }),
  );
  await page.route(`**${root}/generations?*`, (route) =>
    route.fulfill(
      denied
        ? { status: 403, json: { error: "项目权限已撤销" } }
        : { json: { items: [task], runtime_ready: true } },
    ),
  );
  await page.route(`**${root}/generations/${task.id}`, (route) =>
    route.fulfill({ json: task }),
  );
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  const panel = page.locator(".generation-panel");
  await panel.getByRole("button", { name: "查看任务 aaaaaa" }).click();
  await panel.getByLabel("希望 AI 协助的内容").fill("保留作者未保存的想法");
  const preview = {
    generation_id: task.id,
    summary: "保留人物动机，只修订称呼",
    items: [
      {
        target: { resource_id: "r", text: "🌙旧名推门而入。" },
        result: {
          text: "🌙林舟推门而入。",
          applied: [{ reason: "与候选设定一致" }],
        },
      },
    ],
    unaddressed: [
      {
        node: { kind: "resource", id: "未处理章节", branch_id: "另一分支" },
        action: "REVIEW",
      },
    ],
    untracked: [],
    outside_recorded_closure: [],
  };
  return {
    panel,
    preview,
    url: `**${root}/generations/${task.id}/patches`,
    deny: () => {
      denied = true;
    },
  };
}

test("无正文补丁也可检查并保存历史引用决定，编辑理由后必须重检（受控 UI）", async ({
  page,
}) => {
  const f = await openRepair(page);
  const option = {
    resource_id: "r",
    name: "旧时间线章节",
    source: {
      kind: "entity",
      branch_id: "source",
      id: "lin-1",
      revision: "old",
      hash: "a".repeat(64),
    },
    kind: "representation",
    deleted: true,
  };
  const preview = { ...f.preview, items: [], dependency_options: [option] };
  let saved: unknown = null;
  await page.route(f.url, (route) => {
    if (route.request().method() === "POST") {
      saved = route.request().postDataJSON();
      return route.fulfill({ status: 201, json: { id: "saved-decision" } });
    }
    return route.fulfill({ json: preview });
  });
  await page.route(f.url + "/preview", (route) =>
    route.fulfill({
      json: { ...preview, decisions: route.request().postDataJSON().decisions },
    }),
  );
  await f.panel
    .getByRole("button", { name: "预览局部修订", exact: true })
    .click();
  await expect(
    f.panel.getByRole("option", { name: "仍适用，继续使用当前资料" }),
  ).toHaveCount(0);
  await f.panel
    .getByRole("combobox", { name: "处理方式", exact: true })
    .selectOption("KEEP_HISTORY");
  await f.panel
    .getByLabel("处理理由", { exact: true })
    .fill("改编路线保留旧时间线");
  await expect(
    f.panel.getByRole("button", { name: "保存修订候选", exact: true }),
  ).toBeDisabled();
  await f.panel
    .getByRole("button", { name: "检查这些处理决定", exact: true })
    .click();
  await expect(
    f.panel.getByRole("button", { name: "保存修订候选", exact: true }),
  ).toBeEnabled();
  await f.panel
    .getByLabel("处理理由", { exact: true })
    .fill("保留旧时间线中的人物关系");
  await expect(
    f.panel.getByRole("button", { name: "保存修订候选", exact: true }),
  ).toBeDisabled();
  await f.panel
    .getByRole("button", { name: "检查这些处理决定", exact: true })
    .click();
  await f.panel
    .getByRole("button", { name: "保存修订候选", exact: true })
    .click();
  await expect(
    f.panel.getByRole("button", { name: "修订候选已保存", exact: true }),
  ).toBeDisabled();
  expect(saved).toEqual({
    decisions: [
      {
        resource_id: "r",
        source: option.source,
        kind: "representation",
        action: "KEEP_HISTORY",
        reason: "保留旧时间线中的人物关系",
      },
    ],
  });
  await page.clock.runFor(2500);
  await expect(f.panel.getByLabel("处理理由", { exact: true })).toHaveValue(
    "保留旧时间线中的人物关系",
  );
});

test("局部修订可比较和保存，轮询保留预览与作者输入（受控 UI）", async ({
  page,
}) => {
  const f = await openRepair(page);
  let saves = 0;
  await page.route(f.url, (route) => {
    if (route.request().method() === "POST") {
      saves++;
      return route.fulfill({ status: 201, json: { id: "saved-repair" } });
    }
    return route.fulfill({ json: f.preview });
  });
  await f.panel
    .getByRole("button", { name: "预览局部修订", exact: true })
    .click();
  const preview = f.panel.getByRole("region", { name: "局部修订比较" });
  await expect(preview).toContainText("🌙林舟推门而入。");
  await expect(preview).toContainText("其他分支，需分别处理");
  await page.clock.runFor(5000);
  await expect(preview).toBeVisible();
  await f.panel
    .getByRole("button", { name: "保存修订候选", exact: true })
    .click();
  await expect(
    f.panel.getByRole("button", { name: "修订候选已保存", exact: true }),
  ).toBeDisabled();
  await page.clock.runFor(2500);
  await expect(
    f.panel.getByRole("button", { name: "修订候选已保存", exact: true }),
  ).toBeDisabled();
  expect(saves).toBe(1);
  await expect(f.panel.getByLabel("希望 AI 协助的内容")).toHaveValue(
    "保留作者未保存的想法",
  );
});

test("未确认设定阻止保存，权限失效撤下修订但保留作者输入（受控 UI）", async ({
  page,
}) => {
  const f = await openRepair(page);
  await page.route(f.url, (route) =>
    route.fulfill({
      json: { ...f.preview, save_blocked_reason: "请先确认采用本次设定" },
    }),
  );
  await f.panel
    .getByRole("button", { name: "预览局部修订", exact: true })
    .click();
  await expect(
    f.panel.getByRole("button", { name: "保存修订候选", exact: true }),
  ).toBeDisabled();
  await expect(f.panel).toContainText("请先确认采用本次设定");
  f.deny();
  await page.clock.runFor(2500);
  await expect(
    f.panel.getByRole("region", { name: "局部修订比较" }),
  ).toHaveCount(0);
  await expect(f.panel.getByLabel("希望 AI 协助的内容")).toHaveValue(
    "保留作者未保存的想法",
  );
});

test("合并保留原候选新增资料，重叠冲突展示双方内容并禁止保存（受控 UI）", async ({
  page,
}) => {
  const f = await openRepair(page);
  let conflicting = false;
  let writes = 0;
  await page.route(f.url, (route) => {
    if (route.request().method() === "POST") {
      writes++;
      return route.fulfill({ status: 201, json: { id: "mixed-candidate" } });
    }
    return route.fulfill({
      json: {
        ...f.preview,
        source_release_id: "original-candidate",
        preserved: [
          {
            resource_id: "new",
            name: "作者新增笔记",
            text: "不能丢掉的原候选内容",
            merged: false,
          },
        ],
        ...(conflicting
          ? {
              save_blocked_reason: "同一位置存在冲突，请先编辑原候选。",
              conflicts: [
                {
                  resource_id: "r",
                  name: "初见章节",
                  original: "🌙旧城",
                  proposed: "🌙海港",
                  ai_text: "🌙新城",
                },
              ],
            }
          : {}),
      },
    });
  });
  await f.panel
    .getByRole("button", { name: "预览局部修订", exact: true })
    .click();
  const preserved = f.panel.getByRole("region", { name: "保留的原候选修改" });
  await expect(preserved).toContainText("共保留 1 项原有修改");
  await preserved
    .getByText("作者新增笔记 · 保留原修改", { exact: true })
    .click();
  await expect(
    preserved.getByText("不能丢掉的原候选内容", { exact: true }),
  ).toBeVisible();
  await expect(
    f.panel.getByRole("button", { name: "保存修订候选", exact: true }),
  ).toBeEnabled();
  conflicting = true;
  await f.panel
    .getByRole("button", { name: "预览局部修订", exact: true })
    .click();
  const conflict = f.panel.getByRole("region", { name: "需要处理的合并冲突" });
  await expect(conflict.getByText("🌙海港", { exact: true })).toBeVisible();
  await expect(conflict.getByText("🌙新城", { exact: true })).toBeVisible();
  await expect(
    f.panel.getByRole("button", { name: "保存修订候选", exact: true }),
  ).toBeDisabled();
  expect(writes).toBe(0);
  await expect(f.panel.getByLabel("希望 AI 协助的内容")).toHaveValue(
    "保留作者未保存的想法",
  );
});
