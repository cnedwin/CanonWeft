import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";
import { openIPSection } from "./ip-edit-helper";

test("补充检查范围使用检索原文，旧版本禁选，保存失败保留理由并原操作重试（受控 UI）", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  const projectResponse = await page.request.post("/api/projects", {
    headers,
    data: { name: `漏检范围 ${Date.now()}`, operation_id: crypto.randomUUID() },
  });
  expect(projectResponse.status()).toBe(201);
  const project = await projectResponse.json();
  const root = `/api/projects/${project.id}`;
  const branchResponse = await page.request.post(`${root}/branches`, {
    headers,
    data: { name: "正史", operation_id: crypto.randomUUID() },
  });
  expect(branchResponse.status()).toBe(201);
  const branch = await branchResponse.json();
  let planID = "a".repeat(32);
  const ref = {
    kind: "resource",
    branch_id: branch.id,
    id: "chapter",
    revision: "b".repeat(32),
    hash: "c".repeat(64),
  };
  const reason = "没有姓名但使用了银针，需要检查失去医术后的动作";
  const plan = () => ({
    id: planID,
    branch_id: branch.id,
    source_event_id: "event",
    source_state: "CURRENT",
    stale: false,
    semantic_review: "NOT_RUN",
    changes: [],
    differences: [],
    items: planID.startsWith("a")
      ? []
      : [{ node: ref, action: "REVIEW", via: [] }],
    additional_review: planID.startsWith("a") ? [] : [{ node: ref, reason }],
    untracked: [ref],
    outside_recorded_closure: planID.startsWith("a") ? [ref] : [],
  });
  await page.route(`**${root}/dependencies/catalog`, (route) =>
    route.fulfill({
      json: { items: [{ ref, name: "银针场景", branch_name: "正史" }] },
    }),
  );
  await page.route(`**${root}/changes?branch=*`, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: planID,
            release_id: "",
            changes: 1,
            impacts: 0,
            created_at: "2026-10-26",
            source_event_id: "event",
          },
        ],
      },
    }),
  );
  await page.route(`**${root}/changes/*`, (route) =>
    route.fulfill({ json: plan() }),
  );
  await page.route(
    `**${root}/branches/${branch.id}/knowledge/search`,
    (route) => {
      expect(route.request().postDataJSON().purpose).toBe("critic");
      expect(route.request().postDataJSON().story_time).toBe(6);
      return route.fulfill({
        json: {
          evidence: [
            {
              id: "hit",
              resource_id: ref.id,
              name: "银针场景",
              revision: ref.revision,
              start: 0,
              end: 16,
              text: "青衣人收起银针，没有留下姓名。",
            },
            {
              id: "old-hit",
              resource_id: ref.id,
              name: "旧版场景",
              revision: "old",
              start: 0,
              end: 8,
              text: "旧索引中的场景",
            },
          ],
          omissions: ["检索只覆盖当前分支已建索引"],
        },
      });
    },
  );
  const calls: {
    operation_id: string;
    targets: { node: typeof ref; reason: string }[];
  }[] = [];
  await page.route(`**${root}/changes/*/scope`, (route) => {
    calls.push(route.request().postDataJSON());
    if (calls.length === 1)
      return route.fulfill({
        status: 409,
        json: { error: "版本检查失败，保留选择供重试" },
      });
    planID = "d".repeat(32);
    return route.fulfill({ status: 201, json: plan() });
  });
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
  const scope = page.getByRole("region", { name: "补充检查范围" });
  await scope.getByLabel("可能受影响的线索").fill("青衣 银针");
  await scope.getByLabel("检索故事位置").fill("6");
  await scope
    .getByRole("button", { name: "检索可能遗漏的内容", exact: true })
    .click();
  await expect(scope).toContainText("青衣人收起银针，没有留下姓名。");
  await expect(
    scope.getByRole("button", {
      name: "来源版本已变化，请刷新目录",
      exact: true,
    }),
  ).toBeDisabled();
  await scope
    .getByRole("button", { name: "选择这个检查对象", exact: true })
    .click();
  await scope.getByLabel("纳入理由 银针场景", { exact: true }).fill(reason);
  await scope
    .getByRole("button", { name: "保存补充检查清单", exact: true })
    .click();
  await expect(scope.getByRole("alert")).toContainText("版本检查失败");
  await expect(
    scope.getByLabel("纳入理由 银针场景", { exact: true }),
  ).toHaveValue(reason);
  await scope
    .getByRole("button", { name: "保存补充检查清单", exact: true })
    .click();
  await expect(
    page.getByLabel("已保存的影响清单", { exact: true }),
  ).toHaveValue(planID);
  expect(calls).toHaveLength(2);
  expect(calls[1]).toEqual(calls[0]);
  expect(calls[1]?.targets).toEqual([{ node: ref, reason }]);
  await expect(page.getByTestId("impact-plan")).toContainText(
    `作者补充检查 · 正史 / 银针场景`,
  );
  await expect(page.getByTestId("impact-plan")).toContainText(
    "独立审查按所选批次记录",
  );
});
