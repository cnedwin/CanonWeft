import { loginAsOwner } from "./owner-session";
import { openIPSection, saveReasonedCandidate } from "./ip-edit-helper";
import { test, expect } from "@playwright/test";

test("依赖和冻结经确认发布，变更清单可刷新回读且不会改写正文", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await loginAsOwner(page);
  const name = `变更关联 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByRole("button", { name: "新增角色或实体" }).click();
  await page.getByLabel("实体名称", { exact: true }).fill("林舟");
  await page.getByLabel("实体简介").fill("长发，青年医师");
  await expect(page.getByText("草稿已自动暂存，尚未采用")).toBeVisible();
  await saveReasonedCandidate(page);
  await page.getByRole("button", { name: "确认采用此候选" }).first().click();
  await page.getByRole("button", { name: "发布此版本" }).first().click();
  await page.getByLabel("资料名称", { exact: true }).fill("角色形象说明");
  await page.getByRole("button", { name: "创建资料", exact: true }).click();
  await page
    .getByRole("button", { name: "编辑 角色形象说明", exact: true })
    .click();
  await page.getByLabel("角色形象说明 · 待保存内容").fill("保留的形象说明原稿");
  await expect(
    page.getByText("草稿已暂存；发布后才成为正式资料"),
  ).toBeVisible();
  await page.getByRole("button", { name: "保存联合候选", exact: true }).click();
  await expect(
    page.getByText("联合候选已保存。请展开对照检查，再确认整组内容。", {
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  await page
    .getByRole("button", { name: "确认采用整组内容", exact: true })
    .click();
  await page.getByRole("button", { name: "发布整组内容", exact: true }).click();
  await page.getByRole("button", { name: "收起此资料", exact: true }).click();
  await page.getByText("依赖、冻结与变更影响", { exact: true }).click();
  await page
    .getByLabel("要维护依赖的资料")
    .selectOption({ label: "角色形象说明" });
  await page.getByRole("button", { name: "载入资料依赖", exact: true }).click();
  await page.getByLabel("依赖来源", { exact: true }).selectOption({ index: 1 });
  await page
    .getByLabel("关联类型", { exact: true })
    .selectOption("representation");
  await page
    .getByLabel("关联理由", { exact: true })
    .fill("以林舟的形象设定为依据");
  await page.getByRole("button", { name: "添加依赖", exact: true }).click();
  await page.getByLabel("冻结此产物，后续修改须由所有者裁决").check();
  let releaseRefresh: () => void = () => {};
  const refreshGate = new Promise<void>((resolve) => {
    releaseRefresh = resolve;
  });
  let finishRefresh: () => void = () => {};
  const refreshFinished = new Promise<void>((resolve) => {
    finishRefresh = resolve;
  });
  const releaseListURL = /\/releases\?branch=/;
  await page.route(releaseListURL, async (route) => {
    try {
      await refreshGate;
      await route.continue();
    } finally {
      finishRefresh();
    }
  });
  await page.getByRole("button", { name: "保存依赖候选", exact: true }).click();
  try {
    await expect(
      page.getByText("依赖候选已保存，请在下方检查对照、确认采用并发布。"),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "检查联合候选", exact: true }).first(),
    ).toBeDisabled();
  } finally {
    releaseRefresh();
    await refreshFinished;
    await page.unroute(releaseListURL);
  }
  await page
    .getByRole("button", { name: "放弃编辑并收起", exact: true })
    .click();
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  await expect(page.getByTestId("release-preview")).toContainText(
    "已冻结 · 1 项依赖",
  );
  await page
    .getByRole("button", { name: "确认采用整组内容", exact: true })
    .click();
  await page.getByRole("button", { name: "发布整组内容", exact: true }).click();
  await openIPSection(page, "设定草稿");
  await page.getByLabel("实体简介").fill("短发，左眼有伤痕，青年医师");
  await expect(page.getByText("草稿已自动暂存，尚未采用")).toBeVisible();
  await saveReasonedCandidate(page);
  await page.getByLabel("待分析的设定候选").selectOption({ index: 1 });
  await page
    .getByRole("button", { name: "生成变更影响清单", exact: true })
    .click();
  await expect(page.getByTestId("impact-plan")).toContainText(
    "需要裁决：冻结内容",
  );
  await expect(page.getByTestId("impact-plan")).toContainText(
    "尚未完成独立语义漏检",
  );
  const planID = await page.getByLabel("已保存的影响清单").inputValue();
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("依赖、冻结与变更影响", { exact: true }).click();
  await page.getByLabel("已保存的影响清单").selectOption(planID);
  await page
    .getByRole("button", { name: "打开并检查清单时效", exact: true })
    .click();
  await expect(page.getByTestId("impact-plan")).toContainText(
    "角色形象说明 · 需要裁决",
  );
  await page
    .getByRole("button", { name: "编辑 角色形象说明", exact: true })
    .click();
  await expect(page.getByLabel("角色形象说明 · 待保存内容")).toHaveValue(
    "保留的形象说明原稿",
  );
  await page
    .getByRole("button", {
      name: "用当前正式内容替换这份私人草稿",
      exact: true,
    })
    .click();
  await expect(
    page.getByText("草稿已暂存；发布后才成为正式资料"),
  ).toBeVisible();
  await expect(page.getByTestId("impact-plan")).toContainText(
    "短发，左眼有伤痕",
  );
  await page
    .getByRole("button", { name: "确认采用此候选", exact: true })
    .first()
    .click();
  await page.getByLabel("同步发布设定").selectOption({ index: 1 });
  await page
    .getByLabel("将已记录的关联指向本组的新版本（固定历史版本的关联保持原样）")
    .check();
  await page
    .getByLabel("冻结资料修订说明（随候选保存）")
    .fill("经检查影响范围，同步修订短发和伤痕描写");
  await page
    .getByLabel("角色形象说明 · 待保存内容")
    .fill("林舟留短发，左眼有伤痕。");
  await expect(
    page.getByText("草稿已暂存；发布后才成为正式资料"),
  ).toBeVisible();
  await page.getByRole("button", { name: "保存联合候选", exact: true }).click();
  await expect(
    page.getByText("联合候选已保存。请展开对照检查，再确认整组内容。", {
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  await expect(page.getByTestId("release-preview")).toContainText(
    "经检查影响范围，同步修订短发和伤痕描写",
  );
  await page.getByLabel("待分析的资料候选").selectOption({ index: 1 });
  await page
    .getByRole("button", { name: "生成资料变更清单", exact: true })
    .click();
  await expect(page.getByTestId("impact-plan")).toContainText(
    "保留的形象说明原稿",
  );
  await expect(page.getByTestId("impact-plan")).toContainText(
    "林舟留短发，左眼有伤痕。",
  );
  // This group also changes IP and therefore requires an independent review.
  // The complete qualified joint-publication path is exercised in the actual
  // service suite; this no-model page must preserve the original materials.
  await expect(
    page.getByRole("button", { name: "确认采用整组内容", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByTestId("release-preview").getByTestId("semantic-review-panel"),
  ).toContainText("独立语义审查");
  await expect(page.getByTestId("published-snapshot")).toContainText(
    "长发，青年医师",
  );
  // Explicit manual IP publication is still free. It makes the previous joint
  // plan stale instead of silently publishing the frozen dependent resource.
  await page
    .getByRole("button", { name: "发布此版本", exact: true })
    .first()
    .click();
  await expect(page.getByTestId("published-snapshot")).toContainText(
    "短发，左眼有伤痕",
  );
  await page
    .getByRole("button", { name: "打开并检查清单时效", exact: true })
    .click();
  await expect(page.getByTestId("impact-plan")).toContainText(
    "这是历史清单，请重新分析最新候选",
  );
  expect(errors).toEqual([]);
  await page
    .getByTestId("dependency-panel")
    .screenshot({ path: "../../work/dependency-impact.png" });
});
