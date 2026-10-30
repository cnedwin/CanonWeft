import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";
import { openIPSection } from "./ip-edit-helper";

test("修订草稿迟到回执不覆盖新输入，多标签页冲突保留两边内容", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  const name = `私人修订 ${Date.now()}`;
  const p = await page.request.post("/api/projects", {
    headers,
    data: { name, operation_id: crypto.randomUUID() },
  });
  expect(p.status()).toBe(201);
  const project = await p.json();
  const b = await page.request.post(`/api/projects/${project.id}/branches`, {
    headers,
    data: { name: "正史", operation_id: crypto.randomUUID() },
  });
  expect(b.status()).toBe(201);
  const branch = await b.json();
  const path = `/api/projects/${project.id}/branches/${branch.id}/patch-draft`;
  async function open(target: typeof page) {
    await target.goto("/");
    await target.getByRole("button", { name, exact: true }).click();
    await target.getByRole("button", { name: "打开设定库" }).click();
    await target.getByRole("button", { name: "正史", exact: true }).click();
    await openIPSection(target, "版本与分支");
    await target.getByText("局部修订", { exact: true }).click();
    await expect(target.getByLabel("原文片段", { exact: true })).toBeEnabled();
  }
  await open(page);
  let releaseReply!: () => void;
  const gate = new Promise<void>((resolve) => {
    releaseReply = resolve;
  });
  let markCommitted!: () => void;
  const committed = new Promise<void>((resolve) => {
    markCommitted = resolve;
  });
  let first = true;
  await page.route(`**${path}`, async (route) => {
    if (route.request().method() !== "PUT" || !first) {
      await route.continue();
      return;
    }
    first = false;
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    markCommitted();
    await gate;
    await route.fulfill({ response });
  });
  await page.getByLabel("原文片段", { exact: true }).fill("第一次输入");
  await committed;
  await page
    .getByLabel("原文片段", { exact: true })
    .fill("等待回执时继续输入🌙");
  releaseReply();
  await expect(page.getByTestId("patch-draft-status")).toHaveText(
    "修订草稿已自动暂存",
  );
  await expect(page.getByLabel("原文片段", { exact: true })).toHaveValue(
    "等待回执时继续输入🌙",
  );
  expect((await (await page.request.get(path)).json()).content.snippet).toBe(
    "等待回执时继续输入🌙",
  );
  const second = await page.context().newPage();
  await open(second);
  await second.getByLabel("原文片段", { exact: true }).fill("第二页面的新稿");
  await expect(second.getByTestId("patch-draft-status")).toHaveText(
    "修订草稿已自动暂存",
  );
  await page
    .getByLabel("原文片段", { exact: true })
    .fill("第一页面仍保留的不同修改");
  await expect(
    page.getByText(
      "另一页面已保存新版本，本页编辑仍在。可先复制本页内容，再选择读取最新草稿。",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(page.getByLabel("原文片段", { exact: true })).toHaveValue(
    "第一页面仍保留的不同修改",
  );
  expect((await (await page.request.get(path)).json()).content.snippet).toBe(
    "第二页面的新稿",
  );
  await page
    .getByRole("button", { name: "放弃本页编辑，读取最新草稿", exact: true })
    .click();
  await expect(page.getByLabel("原文片段", { exact: true })).toHaveValue(
    "第二页面的新稿",
  );
  await second.close();
});

test("正文撤回保留后来创作，保存为待独立审查的新候选", async ({ page }) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  async function post(path: string, data: unknown, status = 201) {
    const response = await page.request.post(path, { headers, data });
    expect(response.status(), await response.text()).toBe(status);
    return response.json();
  }
  const name = `撤回正文 ${Date.now()}`;
  const project = await post("/api/projects", {
    name,
    operation_id: crypto.randomUUID(),
  });
  const root = `/api/projects/${project.id}`;
  const branch = await post(`${root}/branches`, {
    name: "正史",
    operation_id: crypto.randomUUID(),
  });
  const resource = await post(`${root}/resources`, {
    name: "旧城场景",
    kind: "note",
    branch_id: branch.id,
    operation_id: crypto.randomUUID(),
  });
  async function publish(head: string, text: string) {
    const release = await post(`${root}/releases`, {
      branch_id: branch.id,
      expected_ip_head: "",
      operation_id: crypto.randomUUID(),
      updates: [{ resource_id: resource.id, expected_head: head, text }],
    });
    await post(`${root}/releases/${release.id}/adopt`, {}, 200);
    await post(`${root}/releases/${release.id}/publish`, {}, 200);
    return release.id as string;
  }
  const original = await publish("", "旧城。\n原来的结尾。");
  const changed = await publish(original, "新城。\n原来的结尾。");
  const latest = await publish(changed, "新城。\n作者后来重写的结尾。");
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("局部修订", { exact: true }).click();
  await page.getByText("撤回已发布的正文改动", { exact: true }).click();
  await page
    .getByLabel("要撤回正文改动的版本", { exact: true })
    .selectOption(changed);
  await page
    .getByLabel("撤回正文理由", { exact: true })
    .fill("撤回地名，保留结尾修改");
  await page
    .getByRole("button", { name: "计算保留后续编辑的撤回方案", exact: true })
    .click();
  const preview = page.getByTestId("patch-group-preview");
  await expect(preview).toContainText("旧城。\n作者后来重写的结尾。");
  await page.getByRole("button", { name: "保存修订候选", exact: true }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "修订候选已保存" }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  await expect(page.getByTestId("release-preview")).toContainText(
    "旧城。\n作者后来重写的结尾。",
  );
  await expect(
    page.getByRole("button", { name: "确认采用整组内容", exact: true }),
  ).toBeDisabled();
  const current = await (
    await page.request.get(`${root}/resources/${resource.id}`)
  ).json();
  expect(current.head).toBe(latest);
  expect(current.text).toBe("新城。\n作者后来重写的结尾。");
});

test("局部修订展示整组差异，保存后必须独立审查且保留旧正文", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  async function post(path: string, data: unknown, expected = 201) {
    const response = await page.request.post(path, { headers, data });
    expect(response.status(), await response.text()).toBe(expected);
    return response.json();
  }
  const name = `局部修订 ${Date.now()}`;
  const project = await post("/api/projects", {
    name,
    operation_id: crypto.randomUUID(),
  });
  const root = `/api/projects/${project.id}`;
  const branch = await post(`${root}/branches`, {
    name: "正史",
    operation_id: crypto.randomUUID(),
  });
  const resources: { id: string; name: string }[] = [];
  for (const title of ["场景正文", "关联记录"]) {
    resources.push(
      await post(`${root}/resources`, {
        name: title,
        branch_id: branch.id,
        kind: "note",
        operation_id: crypto.randomUUID(),
      }),
    );
  }
  const original = "序章：林舟🌙走进旧城。\n他没有回头。";
  const release = await post(`${root}/releases`, {
    branch_id: branch.id,
    expected_ip_head: "",
    operation_id: crypto.randomUUID(),
    updates: resources.map((resource) => ({
      resource_id: resource.id,
      expected_head: "",
      text: original,
    })),
  });
  await post(`${root}/releases/${release.id}/adopt`, {}, 200);
  await post(`${root}/releases/${release.id}/publish`, {}, 200);
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("局部修订", { exact: true }).click();
  for (const resource of resources) {
    await page
      .getByLabel("修订目标资料", { exact: true })
      .selectOption(resource.id);
    await page.getByLabel("原文片段", { exact: true }).fill("旧城");
    await page.getByLabel("替换内容", { exact: true }).fill("新城");
    await page
      .getByLabel("局部修订理由", { exact: true })
      .fill("统一地点名称，保留其他描写");
    await expect(page.getByTestId("patch-draft-status")).toHaveText(
      "修订草稿已自动暂存",
    );
    await page.reload();
    await page.getByRole("button", { name, exact: true }).click();
    await page.getByRole("button", { name: "打开设定库" }).click();
    await page.getByRole("button", { name: "正史", exact: true }).click();
    await openIPSection(page, "版本与分支");
    await page.getByText("局部修订", { exact: true }).click();
    await expect(page.getByLabel("原文片段", { exact: true })).toHaveValue(
      "旧城",
    );
    await expect(page.getByLabel("局部修订理由", { exact: true })).toHaveValue(
      "统一地点名称，保留其他描写",
    );
    await page.getByRole("button", { name: "加入修订组", exact: true }).click();
  }
  await page.getByRole("button", { name: "核对整组修订", exact: true }).click();
  const preview = page.getByTestId("patch-group-preview");
  await expect(preview).toContainText("序章：林舟🌙走进新城。");
  await expect(preview).toContainText("他没有回头。");
  await page.getByRole("button", { name: "保存修订候选", exact: true }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "修订候选已保存" }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "检查联合候选", exact: true })
    .first()
    .click();
  await expect(page.getByTestId("release-preview")).toContainText("新城");
  await expect(
    page.getByRole("button", { name: "确认采用整组内容", exact: true }),
  ).toBeDisabled();
  for (const resource of resources) {
    const current = await (
      await page.request.get(`${root}/resources/${resource.id}`)
    ).json();
    expect(current.head).toBe(release.id);
    expect(current.text).toBe(original);
  }
});
