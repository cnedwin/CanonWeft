import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";

test("跨分支发布先对照完整内容，丢失回复保留同一发布请求（受控发布接口）", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  const post = async (path: string, data: unknown, status = 201) => {
    const r = await page.request.post(path, { headers, data });
    expect(r.status()).toBe(status);
    return r.json();
  };
  const p = await post("/api/projects", {
    name: `联合发布 ${Date.now()}`,
    operation_id: crypto.randomUUID(),
  });
  const root = `/api/projects/${p.id}`;
  const branches = [],
    releases = [];
  for (const name of ["小说正史", "漫画改编"]) {
    const b = await post(`${root}/branches`, {
      name,
      operation_id: crypto.randomUUID(),
    });
    branches.push(b);
    const r = await post(`${root}/resources`, {
      branch_id: b.id,
      name: `${name}章节`,
      kind: "note",
      operation_id: crypto.randomUUID(),
    });
    const release = await post(`${root}/releases`, {
      branch_id: b.id,
      operation_id: crypto.randomUUID(),
      updates: [
        {
          resource_id: r.id,
          expected_head: "",
          text: `${name}修订后的完整正文`,
        },
      ],
    });
    await post(`${root}/releases/${release.id}/adopt`, {}, 200);
    releases.push(release);
  }
  const requests: Record<string, unknown>[] = [];
  await page.route(`**${root}/releases/joint-publication`, (route) => {
    requests.push(route.request().postDataJSON());
    if (requests.length === 1)
      return route.fulfill({
        status: 500,
        json: { error: "发布回复丢失，请原操作重试" },
      });
    return route.fulfill({
      json: {
        id: "f".repeat(64),
        releases: releases.map((r) => ({ ...r, status: "PUBLISHED" })),
      },
    });
  });
  await page.reload();
  await page.getByRole("button", { name: p.name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "小说正史", exact: true }).click();
  await page.getByText("跨分支一起发布", { exact: true }).click();
  const panel = page.getByRole("group", { name: "跨分支发布选择" });
  for (let i = 0; i < branches.length; i++) {
    await panel
      .getByLabel("选择发布分支", { exact: true })
      .selectOption(branches[i].id);
    await panel
      .getByRole("button", { name: "读取该分支已采用候选", exact: true })
      .click();
    await panel
      .getByLabel("选择已采用清单", { exact: true })
      .selectOption(releases[i].id);
    await panel
      .getByRole("button", { name: "加入并查看完整对照", exact: true })
      .click();
  }
  const publish = panel.getByRole("button", {
    name: "确认这组内容并一起发布",
    exact: true,
  });
  await expect(publish).toBeDisabled();
  await expect(panel).toContainText("小说正史修订后的完整正文");
  await expect(panel).toContainText("漫画改编修订后的完整正文");
  await panel.getByLabel("我已核对所有分支的变更内容", { exact: true }).check();
  await publish.click();
  await expect(panel.getByRole("alert")).toContainText("发布回复丢失");
  await publish.click();
  await expect(panel.getByRole("status")).toContainText("2 份清单已一起发布");
  expect(requests).toHaveLength(2);
  expect(requests[1]).toEqual(requests[0]);
  expect(requests[0]?.releases).toEqual(
    releases
      .map((r) => ({ release_id: r.id, manifest_hash: r.hash }))
      .sort((a, b) => a.release_id.localeCompare(b.release_id)),
  );
  await panel.screenshot({ path: "../../work/joint-publication-preview.png" });
});

test("恢复的联合发布历史可重试查看保存内容，版本不符时拒绝展示（受控历史接口）", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  const post = async (path: string, data: unknown) => {
    const response = await page.request.post(path, { headers, data });
    expect(response.status()).toBe(201);
    return response.json();
  };
  const project = await post("/api/projects", {
    name: `历史对照 ${Date.now()}`,
    operation_id: crypto.randomUUID(),
  });
  const root = `/api/projects/${project.id}`;
  const branch = await post(`${root}/branches`, {
    name: "恢复的小说",
    operation_id: crypto.randomUUID(),
  });
  const group = "a".repeat(64);
  const release = "b".repeat(32);
  const hash = "c".repeat(64);
  let reads = 0,
    detailReads = 0,
    writes = 0;
  await page.route(`**${root}/joint-publications?**`, (route) => {
    reads++;
    if (reads === 1)
      return route.fulfill({
        status: 500,
        json: { error: "历史暂时无法读取，请重试" },
      });
    return route.fulfill({
      json: {
        items: [
          {
            id: group,
            recovered: true,
            members: [
              {
                release_id: release,
                manifest_hash: hash,
                branch_id: branch.id,
              },
              {
                release_id: "d".repeat(32),
                manifest_hash: "e".repeat(64),
                branch_id: "改编分支",
              },
            ],
          },
        ],
        next: "",
      },
    });
  });
  await page.route(`**${root}/releases/${release}`, (route) => {
    detailReads++;
    return route.fulfill({
      json: {
        id: release,
        hash: detailReads === 1 ? "wrong-version" : hash,
        manifest: { branch_id: branch.id },
        contents: [
          {
            resource_id: "saved-scene",
            text: "恢复前共同发布的场景：林舟选择守护故人。",
          },
        ],
      },
    });
  });
  page.on("request", (request) => {
    if (request.method() !== "GET" && request.url().includes(root)) writes++;
  });
  await page.reload();
  await page.getByRole("button", { name: project.name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "恢复的小说", exact: true }).click();
  await page.getByText("联合发布历史", { exact: true }).click();
  const panel = page.getByRole("region", {
    name: "联合发布历史记录",
    exact: true,
  });
  await expect(panel.getByRole("alert")).toContainText("历史暂时无法读取");
  await panel
    .getByRole("button", { name: "刷新历史记录", exact: true })
    .click();
  await expect(panel).toContainText("从原项目恢复的历史 · 恢复时没有重新发布");
  await panel
    .getByRole("button", { name: `查看历史版本 ${release}`, exact: true })
    .click();
  await expect(panel.getByRole("alert")).toContainText("历史版本与记录不一致");
  await expect(panel).not.toContainText("林舟选择守护故人");
  await panel
    .getByRole("button", { name: "重试读取内容", exact: true })
    .click();
  await expect(
    panel.getByRole("region", { name: "联合发布历史内容" }),
  ).toContainText("林舟选择守护故人");
  expect(writes).toBe(0);
  await panel.screenshot({ path: "../../work/joint-history-preview.png" });
});
