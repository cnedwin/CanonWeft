import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";
import { openIPSection } from "./ip-edit-helper";

test("独立影响审查固定本批选择，失败原操作重试，证据与失效状态可见（受控UI）", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  const p = await page.request.post("/api/projects", {
    headers,
    data: { name: `影响审查 ${Date.now()}`, operation_id: crypto.randomUUID() },
  });
  expect(p.status()).toBe(201);
  const project = await p.json(),
    root = `/api/projects/${project.id}`;
  const b = await page.request.post(`${root}/branches`, {
    headers,
    data: { name: "正史", operation_id: crypto.randomUUID() },
  });
  expect(b.status()).toBe(201);
  const createdBranch = await b.json();
  const branch = await (
      await page.request.get(`${root}/branches/${createdBranch.id}`)
    ).json(),
    planID = "a".repeat(32),
    taskID = "b".repeat(32);
  const refs = [
    "chapter",
    "unselected",
    ...Array.from({ length: 32 }, (_, i) => `extra-${i}`),
  ].map((id) => ({
    kind: "resource",
    branch_id: branch.id,
    id,
    revision: "c".repeat(32),
    hash: "d".repeat(64),
  }));
  const plan = {
    id: planID,
    branch_id: branch.id,
    stale: false,
    semantic_review: "NOT_RUN",
    source_event_id: "event",
    source_state: "CURRENT",
    changes: [],
    differences: [],
    items: refs.map((node) => ({ node, action: "REVIEW", via: [] })),
    untracked: [],
    outside_recorded_closure: [],
  };
  await page.route(`**${root}/dependencies/catalog`, (r) =>
    r.fulfill({
      json: {
        items: refs.map((ref, i) => ({
          ref,
          name: i ? "商队场景" : "银针场景",
          branch_name: "正史",
        })),
      },
    }),
  );
  await page.route(`**${root}/changes?branch=*`, (r) =>
    r.fulfill({
      json: {
        items: [
          {
            id: planID,
            release_id: "",
            impacts: 2,
            changes: 1,
            source_event_id: "event",
          },
        ],
      },
    }),
  );
  await page.route(`**${root}/changes/${planID}`, (r) =>
    r.fulfill({ json: plan }),
  );
  await page.route(`**${root}/qualification/bindings`, (r) =>
    r.fulfill({
      json: {
        items: [
          {
            id: "qualified",
            enabled: true,
            current: true,
            valid: true,
            task_kind: "shared.change-impact",
            qualification: {
              frozen: {
                input: { role: "critic", max_output_tokens: 1024 },
                config: { id: "model", revision: 1, name: "独立审查模型" },
              },
            },
          },
        ],
      },
    }),
  );
  await page.route(`**${root}/generation-options`, (r) =>
    r.fulfill({
      json: {
        items: [
          {
            role: "critic",
            name: "独立审查模型",
            price: {
              id: "price",
              currency: "USD",
              connection_id: "model",
              connection_revision: 1,
            },
          },
        ],
      },
    }),
  );
  const requests: Record<string, unknown>[] = [];
  let saved = false,
    started = false,
    stale = false,
    moved = false;
  const task = () => ({
    id: taskID,
    state: started ? "SUCCEEDED" : "PREPARED",
    paused: false,
    cancelled: false,
    frozen: { estimate_micros: 100, price: { currency: "USD" } },
  });
  const prepared = new Map<string, ReturnType<typeof task>>();
  await page.route(`**${root}/changes/${planID}/inspection-coverage*`, (r) =>
    r.fulfill({
      json: {
        plan_id: planID,
        branch_id: branch.id,
        scope: "仅清单内资料，不证明全书无遗漏",
        items: refs.map((ref) => ({
          ref,
          state: prepared.has(ref.id) ? "PENDING" : "UNCHECKED",
          runs: [],
          checked: false,
          pending: prepared.has(ref.id),
        })),
        checked: 0,
        pending: prepared.size,
        findings: 0,
        uncertain: 0,
        needs_attention: 0,
        unreadable_runs: 0,
        outside_scope: 3,
        untracked: 2,
        automatic_ready: true,
        all_listed_checked: false,
        next_batch: refs
          .filter((ref) => !prepared.has(ref.id))
          .slice(0, 16)
          .map((ref) => ref.id),
      },
    }),
  );
  await page.route(`**${root}/branches/${branch.id}`, (r) =>
    r.fulfill({ json: { ...branch, head: moved ? "moved" : branch.head } }),
  );
  await page.route(`**${root}/changes/${planID}/inspections*`, (r) => {
    if (r.request().method() === "GET")
      return r.fulfill({
        json: { items: saved ? [task()] : [], runtime_ready: true },
      });
    requests.push(r.request().postDataJSON());
    saved = true;
    if (requests.length === 1) {
      moved = true;
      return r.fulfill({
        status: 500,
        json: { error: "回复暂时中断，可原操作重试" },
      });
    }
    return r.fulfill({ status: 201, json: task() });
  });
  await page.route(`**${root}/generations/${taskID}/start`, (r) => {
    started = true;
    return r.fulfill({ json: task() });
  });
  await page.route(`**${root}/generations/${taskID}/change-inspection`, (r) =>
    r.fulfill({
      json: {
        generation_id: taskID,
        plan_id: planID,
        state: task().state,
        current: !stale,
        counted: started && !stale,
        scope: "仅本批固定资料，不证明全书无遗漏。",
        reason: stale
          ? "资格或资料改变，本结果不计入当前覆盖。"
          : "本批审查不具备发布权限。",
        items: [
          {
            ref: refs[0],
            name: "银针场景",
            state: started && !stale ? "UNCERTAIN" : "UNCHECKED",
            issues: [],
          },
        ],
        anchors: [{ key: "change", text: "第5章起失去医术" }],
        report:
          started && !stale
            ? {
                verdict: "UNKNOWN",
                summary: "缺少发生时间，不能确定是否需要修订。",
                issues: [
                  {
                    target_key: "resource:chapter",
                    severity: "unknown",
                    explanation: "原文没有说明发生时间",
                    anchor_keys: ["change"],
                  },
                ],
              }
            : undefined,
      },
    }),
  );
  await page.reload();
  await page.getByRole("button", { name: project.name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("依赖、冻结与变更影响", { exact: true }).click();
  await page
    .getByLabel("已保存的影响清单", { exact: true })
    .selectOption(planID);
  await page
    .getByRole("button", { name: "打开并检查清单时效", exact: true })
    .click();
  const panel = page.getByRole("region", { name: "独立变更影响审查" });
  await panel.getByRole("checkbox", { name: /银针场景/ }).check();
  await expect(
    panel.getByLabel("审查目标分支", { exact: true }),
  ).toBeDisabled();
  const prepare = panel.getByRole("button", {
    name: "准备本批审查并估算费用",
    exact: true,
  });
  await prepare.click();
  await expect(panel.getByRole("alert")).toContainText("原操作重试");
  await expect(panel.getByRole("checkbox", { name: /银针场景/ })).toBeChecked();
  await prepare.click();
  expect(requests).toHaveLength(2);
  expect(requests[1]).toEqual(requests[0]);
  expect(requests[1]?.resource_ids).toEqual(["chapter"]);
  expect(requests[1]?.expected_ip_head).toBe("");
  const result = panel.getByRole("region", { name: "本批审查结果" });
  await expect(result).toContainText("未计入检查");
  await result
    .getByRole("button", { name: "按估算启动审查", exact: true })
    .click();
  await expect(result).toContainText("依据不足，待进一步核查");
  await expect(result).not.toContainText("商队场景");
  await result.getByText("查看依据 change", { exact: true }).click();
  await expect(result).toContainText("第5章起失去医术");
  stale = true;
  await result
    .getByRole("button", { name: "刷新本批结果", exact: true })
    .click();
  await expect(result).toContainText("本结果不计入当前覆盖");
  await expect(result).not.toContainText("原文没有说明发生时间");
  // Automatic preparation spans batches, freezes a failed request and never starts calls.
  prepared.set("chapter", task());
  let starts = 0,
    automaticAttempts = 0;
  const automaticRequests: Record<string, unknown>[] = [];
  await page.route(`**${root}/generations/*/start`, (r) => {
    starts++;
    return r.fulfill({ status: 500, json: { error: "不应自动启动" } });
  });
  await page.route(`**${root}/changes/${planID}/inspections*`, (r) => {
    if (r.request().method() === "GET")
      return r.fulfill({
        json: {
          items: [
            ...new Map([...prepared.values()].map((t) => [t.id, t])).values(),
          ],
          runtime_ready: true,
        },
      });
    const input = r.request().postDataJSON();
    automaticRequests.push(input);
    automaticAttempts++;
    const id = String(input.operation_id);
    const made = { ...task(), id, state: "PREPARED" };
    for (const resource of input.resource_ids) prepared.set(resource, made);
    if (automaticAttempts === 1)
      return r.fulfill({ status: 500, json: { error: "自动准备回复中断" } });
    return r.fulfill({ status: 201, json: made });
  });
  await panel
    .getByRole("button", { name: "刷新审查条件与记录", exact: true })
    .click();
  const auto = panel.getByRole("button", {
    name: "自动准备未检查批次",
    exact: true,
  });
  await auto.click();
  await expect(panel.getByRole("alert")).toContainText("自动准备回复中断");
  await auto.click();
  await expect(panel.getByText(/本次已准备 3 批/)).toBeVisible();
  expect(automaticRequests).toHaveLength(4);
  expect(automaticRequests[1]).toEqual(automaticRequests[0]);
  expect(
    automaticRequests.map((r) => (r.resource_ids as string[]).length),
  ).toEqual([16, 16, 16, 1]);
  expect(starts).toBe(0);
  await expect(
    panel.getByRole("region", { name: "跨批审查覆盖" }),
  ).toContainText("待启动或执行 34");
  await expect(auto).toBeDisabled();
  // Stop after the in-flight preparation; already prepared tasks remain visible.
  prepared.clear();
  let releaseRequest!: () => void;
  const held = new Promise<void>((resolve) => {
    releaseRequest = resolve;
  });
  let stopRequests = 0;
  await page.route(`**${root}/changes/${planID}/inspections*`, async (r) => {
    if (r.request().method() === "GET")
      return r.fulfill({
        json: {
          items: [
            ...new Map([...prepared.values()].map((t) => [t.id, t])).values(),
          ],
          runtime_ready: true,
        },
      });
    stopRequests++;
    const input = r.request().postDataJSON();
    await held;
    const made = {
      ...task(),
      id: String(input.operation_id),
      state: "PREPARED",
    };
    for (const resource of input.resource_ids) prepared.set(resource, made);
    return r.fulfill({ status: 201, json: made });
  });
  await panel
    .getByRole("button", { name: "刷新审查条件与记录", exact: true })
    .click();
  await auto.click();
  await expect.poll(() => stopRequests).toBe(1);
  await panel
    .getByRole("button", { name: "停止准备后续批次", exact: true })
    .click();
  releaseRequest();
  await expect(panel.getByText(/本次已准备 1 批/)).toBeVisible();
  await expect(auto).toBeEnabled();
  expect(stopRequests).toBe(1);
  expect(prepared.size).toBe(16);
  expect(starts).toBe(0);
  await panel.screenshot({ path: "../../work/change-inspection-preview.png" });
});
