import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";

test("跨分支候选显示上游原文，明确选择改绑，重试不换输入且保留历史固定引用", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  async function post(path: string, data: unknown, status = 201) {
    const response = await page.request.post(path, { headers, data });
    expect(response.status(), await response.text()).toBe(status);
    return response.json();
  }
  const project = await post("/api/projects", {
    name: `协同候选 ${Date.now()}`,
    operation_id: crypto.randomUUID(),
  });
  const root = `/api/projects/${project.id}`;
  const novel = await post(`${root}/branches`, {
    name: "小说",
    operation_id: crypto.randomUUID(),
  });
  const comic = await post(`${root}/branches`, {
    name: "改编",
    operation_id: crypto.randomUUID(),
  });
  const source = await post(`${root}/resources`, {
    name: "小说新情节",
    kind: "note",
    branch_id: novel.id,
    operation_id: crypto.randomUUID(),
  });
  const original = await post(`${root}/releases`, {
    branch_id: novel.id,
    operation_id: crypto.randomUUID(),
    updates: [{ resource_id: source.id, text: "原小说情节" }],
  });
  await post(`${root}/releases/${original.id}/adopt`, {}, 200);
  await post(`${root}/releases/${original.id}/publish`, {}, 200);
  const catalog = await (
    await page.request.get(`${root}/dependencies/catalog`)
  ).json();
  const oldRef = catalog.items.find(
    (entry: { ref: { id: string } }) => entry.ref.id === source.id,
  ).ref;
  const upstream = await post(`${root}/releases`, {
    branch_id: novel.id,
    operation_id: crypto.randomUUID(),
    updates: [
      {
        resource_id: source.id,
        expected_head: original.id,
        text: "新版小说：主角舍弃报复，救下旧敌。",
      },
    ],
  });
  const upstreamFull = await (
    await page.request.get(`${root}/releases/${upstream.id}`)
  ).json();
  const adapted = await post(`${root}/resources`, {
    name: "改编场景",
    kind: "note",
    branch_id: comic.id,
    operation_id: crypto.randomUUID(),
  });
  const target = await post(`${root}/releases`, {
    branch_id: comic.id,
    operation_id: crypto.randomUUID(),
    updates: [
      {
        resource_id: adapted.id,
        text: "改编旧稿",
        dependencies: {
          frozen: false,
          inputs: [
            { source: oldRef, kind: "semantic", reason: "保留小说主旨" },
            {
              source: oldRef,
              kind: "evidence",
              reason: "固定旧版对照",
              pinned: true,
            },
          ],
        },
      },
    ],
  });
  const targetFull = await (
    await page.request.get(`${root}/releases/${target.id}`)
  ).json();
  const nextRef = { ...oldRef, revision: upstream.id, hash: "b".repeat(64) };
  await page.route(`**${root}/releases/${upstream.id}/peer-sources`, (route) =>
    route.fulfill({
      json: {
        release: upstreamFull,
        entries: [{ ref: nextRef, name: "小说新情节", branch_name: "小说" }],
        snapshot: { entities: [], assertions: [] },
      },
    }),
  );
  const requests: Record<string, unknown>[] = [];
  const saved = {
    ...targetFull,
    id: "f".repeat(32),
    requires_semantic_review: true,
  };
  await page.route(`**${root}/releases`, (route) => {
    if (route.request().method() !== "POST") return route.continue();
    requests.push(route.request().postDataJSON());
    return requests.length === 1
      ? route.fulfill({
          status: 500,
          json: { error: "保存回复丢失，可按原输入重试" },
        })
      : route.fulfill({ status: 201, json: saved });
  });
  await page.route(`**${root}/releases/${saved.id}`, (route) =>
    route.fulfill({ json: saved }),
  );
  await page.reload();
  await page.getByRole("button", { name: project.name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "改编", exact: true }).click();
  await page.getByText("引用其他分支的新候选", { exact: true }).click();
  const panel = page.locator(".release-peer-panel");
  await panel.getByLabel("本分支候选", { exact: true }).selectOption(target.id);
  await panel.getByLabel("上游分支", { exact: true }).selectOption(novel.id);
  await panel
    .getByRole("button", { name: "加载上游候选", exact: true })
    .click();
  await panel.getByLabel("上游候选", { exact: true }).selectOption(upstream.id);
  await panel
    .getByRole("button", { name: "加入上游预览", exact: true })
    .click();
  await expect(panel).toContainText("主角舍弃报复，救下旧敌");
  const save = panel.getByRole("button", { name: "保存协同候选", exact: true });
  await expect(save).toBeDisabled();
  const links = panel.getByRole("checkbox", { name: /^改编场景 引用/ });
  await expect(links).toHaveCount(1);
  await links.check();
  await panel.getByText("比较并编辑：改编场景", { exact: true }).click();
  await panel
    .getByLabel("新候选正文", { exact: true })
    .fill("改编新稿：保留救下旧敌的高光。 ");
  await panel
    .getByRole("checkbox", {
      name: "已核对上游内容及上述引用变化，保存为新候选",
      exact: true,
    })
    .check();
  await panel.screenshot({ path: "../../work/release-peers-preview.png" });
  await save.click();
  await expect(panel.getByRole("alert")).toContainText("保存回复丢失");
  await save.click();
  await expect(panel.getByRole("status")).toContainText(
    "已保存引用上游新版本的独立候选",
  );
  expect(requests).toHaveLength(2);
  expect(requests[1]).toEqual(requests[0]);
  expect(requests[0]!.peer_releases).toEqual([
    { release_id: upstream.id, manifest_hash: upstream.hash },
  ]);
  const updates = requests[0]!.updates as {
    text: string;
    origin: { parent_release_id: string };
    dependencies: { inputs: { source: unknown; pinned?: boolean }[] };
  }[];
  expect(updates[0]!.text).toContain("保留救下旧敌的高光");
  expect(updates[0]!.origin.parent_release_id).toBe(target.id);
  expect(updates[0]!.dependencies.inputs[0]!.source).toEqual(nextRef);
  expect(updates[0]!.dependencies.inputs[1]!.source).toEqual(oldRef);
  expect(updates[0]!.dependencies.inputs[1]!.pinned).toBe(true);
});
