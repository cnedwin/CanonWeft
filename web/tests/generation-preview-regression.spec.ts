import { test, expect, type Page } from "@playwright/test";
import { openIPTool } from "./ip-edit-helper";

// Real app/login/project/branch; only generation reads and trial choices are
// controlled UI fixtures. These tests do not prove provider streaming.
function gate() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { promise, release };
}

async function openPanel(page: Page, output = false) {
  await page.clock.install();
  await page.clock.pauseAt(new Date(Date.now() + 1000));
  await page.goto("/");
  await page.getByLabel("用户名", { exact: true }).fill("owner");
  await page
    .getByLabel("密码", { exact: true })
    .fill("test-owner-password-123");
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await page
    .getByLabel("项目名称", { exact: true })
    .fill(`预览回归 ${Date.now()}`);
  const created = page.waitForResponse(
    (r) => r.url().endsWith("/api/projects") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  const project = await (await created).json();
  const root = `/api/projects/${project.id}`;
  const first = {
    id: "111111aaaaaaaaaaaaaaaaaaaaaaaaaa",
    state: output ? "SUCCEEDED" : "RUNNING",
    paused: false,
    cancelled: false,
    frozen: {
      stream: true,
      role: "writer",
      context_id: "",
      base_head: "",
      estimate_micros: 100,
      config: { name: "受控界面夹具", model: "fixture", revision: 1 },
      price: { currency: "USD" },
    },
    ...(output
      ? {
          output: {
            text: "任务 A 私人完整输出",
            input_tokens: 5,
            output_tokens: 9,
          },
        }
      : {
          preview: {
            text: "任务 A 私人预览",
            sequence: 1,
            hash: "a".repeat(64),
            provisional: true,
          },
        }),
  };
  const second = {
    ...first,
    id: "222222bbbbbbbbbbbbbbbbbbbbbbbbbb",
    output: undefined,
    preview: {
      text: "任务 B 私人预览",
      sequence: 1,
      hash: "b".repeat(64),
      provisional: true,
    },
  };
  const version = {
    id: "trial-version",
    hash: "c".repeat(64),
    parent_id: "",
    created_at: "2026-10-25T00:00:00Z",
    definition: {
      kind: "agent",
      key: "fixture",
      name: "输入保留试评",
      description: "UI fixture",
      instructions: "UI fixture",
      role: "writer",
      input_schema: { type: "object", properties: {} },
      output_schema: { type: "object" },
      dependencies: [],
    },
    input_schema_text: '{"type":"object"}',
    output_schema_text: '{"type":"object"}',
  };
  await page.route(`**${root}/generation-options`, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            role: "writer",
            name: "界面夹具",
            model: "fixture",
            price: { id: "fixture-price", currency: "USD" },
          },
        ],
      },
    }),
  );
  await page.route(`**${root}/capabilities?*`, (route) =>
    route.fulfill({ json: { items: [version], next_cursor: "" } }),
  );
  await page.route(`**${root}/capabilities/trial-version/manifest`, (route) =>
    route.fulfill({
      json: {
        root: { id: version.id, hash: version.hash, kind: "agent" },
        hash: "d".repeat(64),
        versions: [version],
        trial_ready: true,
        qualified: false,
        findings: [],
      },
    }),
  );
  const state = { listDenied: false, detailDenied: false, holdSecond: false };
  const secondSeen = gate();
  const secondRelease = gate();
  await page.route(`**${root}/generations?*`, (route) =>
    route.fulfill(
      state.listDenied
        ? { status: 403, json: { error: "项目权限已撤销" } }
        : { json: { items: [first, second], runtime_ready: true } },
    ),
  );
  await page.route(`**${root}/generations/${first.id}`, (route) =>
    route.fulfill(
      state.detailDenied
        ? { status: 409, json: { error: "设定已变化，请重新检查" } }
        : { json: first },
    ),
  );
  await page.route(`**${root}/generations/${second.id}`, async (route) => {
    if (state.holdSecond) {
      secondSeen.release();
      await secondRelease.promise;
    }
    await route.fulfill({ json: second });
  });
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await openIPTool(page, "AI候选");
  const panel = page.locator(".generation-panel");
  await panel.getByRole("button", { name: "查看任务 111111" }).click();
  await expect(panel.locator(".candidate-output")).toContainText(
    output ? "任务 A 私人完整输出" : "任务 A 私人预览",
  );
  await panel.getByLabel("候选用途", { exact: true }).selectOption("writer");
  await panel
    .getByLabel("固定角色版本", { exact: true })
    .selectOption(version.id);
  await panel.getByLabel("希望 AI 协助的内容").fill("作者未保存的提示词");
  await panel.getByLabel("完整试评输入").fill('{"draft":"作者未保存的试评"}');
  await panel.getByLabel("最大输出 token").fill("2048");
  return {
    panel,
    state,
    secondSeen,
    secondRelease,
    secondURL: `${root}/generations/${second.id}`,
  };
}

async function expectInputs(page: Page) {
  const panel = page.locator(".generation-panel");
  await expect(panel.getByLabel("希望 AI 协助的内容")).toHaveValue(
    "作者未保存的提示词",
  );
  await expect(panel.getByLabel("完整试评输入")).toHaveValue(
    '{"draft":"作者未保存的试评"}',
  );
  await expect(panel.getByLabel("候选用途", { exact: true })).toHaveValue(
    "writer",
  );
  await expect(panel.getByLabel("固定角色版本", { exact: true })).toHaveValue(
    "trial-version",
  );
  await expect(panel.getByLabel("最大输出 token")).toHaveValue("2048");
}

for (const output of [false, true]) {
  test(`列表 403 清除${output ? "完整输出" : "预览"}并拒绝在途详情，保留作者输入（受控 UI）`, async ({
    page,
  }) => {
    const { panel, state, secondSeen, secondRelease, secondURL } =
      await openPanel(page, output);
    state.holdSecond = true;
    await panel.getByRole("button", { name: "查看任务 222222" }).click();
    await secondSeen.promise;
    state.listDenied = true;
    await page.clock.runFor(2500);
    await expect(panel.getByRole("alert")).toContainText("项目权限已撤销");
    await expect(panel.locator(".candidate-output")).toHaveCount(0);
    await expectInputs(page);
    const late = page.waitForResponse((r) => r.url().endsWith(secondURL));
    secondRelease.release();
    await (await late).finished();
    await page.clock.runFor(1);
    await expect(panel.locator(".candidate-output")).toHaveCount(0);
    await expectInputs(page);
  });
}

test("手动切换 B 的迟到详情不被 A 后台轮询抢走（受控 UI）", async ({
  page,
}) => {
  const { panel, state, secondSeen, secondRelease, secondURL } =
    await openPanel(page);
  state.holdSecond = true;
  await panel.getByRole("button", { name: "查看任务 222222" }).click();
  await secondSeen.promise;
  await page.clock.runFor(2500);
  // The prior preview stays visible while the user's chosen detail is loading.
  await expect(panel.locator(".candidate-output")).toHaveText(
    "任务 A 私人预览",
  );
  const late = page.waitForResponse((r) => r.url().endsWith(secondURL));
  secondRelease.release();
  await (await late).finished();
  await expect(panel.locator(".candidate-output")).toHaveText(
    "任务 B 私人预览",
  );
  await page.clock.runFor(2500);
  await expect(panel.locator(".candidate-output")).toHaveText(
    "任务 B 私人预览",
  );
  await expectInputs(page);
});

test("详情 409 清除当前预览但保留作者输入（受控 UI）", async ({ page }) => {
  const { panel, state } = await openPanel(page);
  state.detailDenied = true;
  await page.clock.runFor(2500);
  await expect(panel.getByRole("alert")).toContainText("设定已变化");
  await expect(panel.locator(".candidate-output")).toHaveCount(0);
  await expectInputs(page);
});
