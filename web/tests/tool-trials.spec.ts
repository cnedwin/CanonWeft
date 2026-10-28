import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("完整工具包在隔离环境中试运行，刷新保留凭证，撤回后不可重用", async ({
  page,
}) => {
  test.setTimeout(90000);
  await loginAsOwner(page);
  const name = `工具验证 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  const created = page.waitForResponse(
    (r) => r.url().endsWith("/api/projects") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  const project = await (await created).json();
  const root = `/api/projects/${project.id}`;
  const headers = { Origin: new URL(page.url()).origin };
  const source = await page.request.post(`${root}/skills`, {
    headers,
    data: {
      operation_id: crypto.randomUUID(),
      markdown:
        "---\nname: local-check\ndescription: Check approved material only.\nlicense: LicenseRef-Private\n---\nReturn a JSON summary.",
    },
  });
  expect(source.status()).toBe(201);
  const initial = await source.json();
  const edited = await page.request.post(`${root}/skills/${initial.id}/edit`, {
    headers,
    data: {
      operation_id: crypto.randomUUID(),
      expected_hash: initial.report.hash,
      changes: [
        {
          path: "main.py",
          text: "import json,sys\nfrom helper import summarize\nprint(json.dumps(summarize(json.load(sys.stdin)),ensure_ascii=False))\n",
        },
        {
          path: "helper.py",
          text: "def summarize(data):\n return {'summary': '已检查：'+data['source']}\n",
        },
      ],
    },
  });
  expect(edited.status()).toBe(201);
  const pkg = await edited.json();
  const schema = (key: string) => ({
    type: "object",
    properties: { [key]: { type: "string" } },
    required: [key],
    additionalProperties: false,
  });
  const saved = await page.request.post(`${root}/capabilities`, {
    headers,
    data: {
      operation_id: crypto.randomUUID(),
      definition: {
        kind: "tool",
        key: "local-material-check",
        name: "材料检查工具",
        description: "完整包只读检查",
        instructions: "按输入检查材料，输出摘要。",
        dependencies: [],
        package_id: pkg.id,
        package_hash: pkg.report.hash,
        input_schema: schema("source"),
        output_schema: schema("summary"),
        tool: {
          runtime: "linux-python",
          entrypoint: "main.py",
          memory_mib: 256,
          timeout_seconds: 10,
          network: false,
        },
      },
    },
  });
  expect(saved.status()).toBe(201);
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  await page.getByRole("button", { name: /材料检查工具 ·/ }).click();
  const panel = page.getByRole("region", { name: "工具隔离试运行" });
  await panel.getByLabel("工具输入（JSON）").fill('{"source":5}');
  await panel.getByRole("button", { name: "开始隔离试运行" }).click();
  await expect(panel.getByRole("alert")).toBeVisible();
  await expect(panel.getByLabel("工具输入（JSON）")).toHaveValue(
    '{"source":5}',
  );
  await page.getByLabel("能力名称", { exact: true }).fill("修改中的工具名称");
  await expect(
    page.getByRole("button", { name: "保存为新能力版本" }),
  ).toBeDisabled();
  await expect(panel.getByLabel("工具输入（JSON）")).toHaveValue(
    '{"source":5}',
  );
  await page.getByRole("button", { name: "放弃能力修改" }).click();
  await expect(panel.getByLabel("工具输入（JSON）")).toHaveValue(
    '{"source":5}',
  );
  await panel
    .getByLabel("工具输入（JSON）")
    .fill('{"source":"这是一段待核对材料"}');
  await panel.getByRole("button", { name: "开始隔离试运行" }).click();
  await expect(panel.locator(".tool-result")).toContainText("试运行通过", {
    timeout: 60000,
  });
  await expect(panel.locator(".tool-result")).toContainText(
    "已检查：这是一段待核对材料",
  );
  // Controlled UI concurrency fixture; the genuine VM run above is separate evidence.
  const records = await (await page.request.get(`${root}/tool-trials`)).json();
  const trialA = records.items[0];
  const detailA = await (
    await page.request.get(`${root}/tool-trials/${trialA.id}`)
  ).json();
  const trialB = {
    ...detailA,
    id: "00000000000000000000000000000001",
    state: "RUNNING",
    output: { ...detailA.output, stdout: "当前选择的第二个任务" },
  };
  await page.route(`**${root}/tool-trials?*`, (route) =>
    route.fulfill({ json: { ...records, items: [trialA, trialB] } }),
  );
  await page.route(`**${root}/tool-trials/${trialB.id}`, (route) =>
    route.fulfill({ json: trialB }),
  );
  await panel.getByRole("button", { name: "刷新试运行记录" }).click();
  await expect(
    panel.getByRole("button", { name: new RegExp(trialB.id) }),
  ).toBeVisible();
  let arrived!: () => void;
  let release!: () => void;
  const seen = new Promise<void>((resolve) => {
    arrived = resolve;
  });
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(`**${root}/tool-trials/${trialA.id}`, async (route) => {
    arrived();
    await released;
    await route.fulfill({ json: detailA });
  });
  await seen;
  await panel.getByRole("button", { name: new RegExp(trialB.id) }).click();
  await expect(panel.locator(".tool-result")).toContainText(
    "当前选择的第二个任务",
  );
  release();
  await page.waitForTimeout(250);
  await expect(panel.locator(".tool-result")).toContainText(
    "当前选择的第二个任务",
  );
  await page.unrouteAll({ behavior: "wait" });
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  await page.getByRole("button", { name: /材料检查工具 ·/ }).click();
  await panel.getByRole("button", { name: /试运行通过 ·/ }).click();
  await expect(panel.locator(".tool-result")).toContainText(
    "已检查：这是一段待核对材料",
  );
  await page.getByText("撤销能力版本", { exact: true }).click();
  await page.getByLabel("能力撤销原因").fill("重新核对方法");
  await page
    .getByRole("button", { name: "撤销此能力版本", exact: true })
    .click();
  await expect(panel.getByRole("alert")).toHaveText(/此结果不可继续使用/);
  await expect(panel.locator(".tool-result")).not.toContainText(
    "已检查：这是一段待核对材料",
  );
  await page.screenshot({
    path: "../.runtime/tool-trials.png",
    fullPage: true,
  });
});
