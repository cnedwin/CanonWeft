import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";
import { openIPSection } from "./ip-edit-helper";

test("组合候选先展示引用冲突，明确改绑后保存且失败不换请求（受控组合接口）", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  async function post(path: string, data: unknown) {
    const r = await page.request.post(path, { headers, data });
    expect(r.status()).toBe(201);
    return r.json();
  }
  const p = await post("/api/projects", {
    name: `组合候选 ${Date.now()}`,
    operation_id: crypto.randomUUID(),
  });
  const root = `/api/projects/${p.id}`;
  const b = await post(`${root}/branches`, {
    name: "正史",
    operation_id: crypto.randomUUID(),
  });
  const releases = [],
    resources = [];
  for (const name of ["前章", "后章"]) {
    const r = await post(`${root}/resources`, {
      branch_id: b.id,
      name,
      kind: "note",
      operation_id: crypto.randomUUID(),
    });
    resources.push(r);
    releases.push(
      await post(`${root}/releases`, {
        branch_id: b.id,
        operation_id: crypto.randomUUID(),
        updates: [{ resource_id: r.id, text: `${name}候选正文` }],
      }),
    );
  }
  const requests: Record<string, unknown>[] = [];
  await page.route(`**${root}/releases/composition/preview`, (r) => {
    const input = r.request().postDataJSON();
    return r.fulfill({
      json: {
        branch_id: b.id,
        expected_ip_head: "",
        ip_candidate_id: "",
        updates: resources.map((item, i) => ({
          resource_id: item.id,
          text: `${i ? "后章" : "前章"}组合正文`,
        })),
        conflicts: input.rebind_following
          ? []
          : [
              {
                resource_id: resources[1].id,
                releases: [releases[1].id],
                reason: "依赖仍跟随旧版本，请明确改绑",
              },
            ],
        rebindings: input.rebind_following
          ? [
              {
                resource_id: resources[1].id,
                before: { id: resources[0].id },
                target: resources[0].id,
              },
            ]
          : [],
      },
    });
  });
  await page.route(`**${root}/releases/composition`, (r) => {
    requests.push(r.request().postDataJSON());
    if (requests.length === 1)
      return r.fulfill({ status: 500, json: { error: "保存回复中断" } });
    return r.fulfill({
      status: 201,
      json: { ...releases[0], id: "f".repeat(32), status: "PREPARED" },
    });
  });
  await page.reload();
  await page.getByRole("button", { name: p.name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("合并本分支的修订批次", { exact: true }).click();
  const panel = page.getByRole("group", { name: "修订批次组合" });
  await panel
    .getByRole("checkbox", { name: new RegExp(releases[0].id) })
    .check();
  await panel
    .getByRole("checkbox", { name: new RegExp(releases[1].id) })
    .check();
  await panel
    .getByRole("button", { name: "预览组合与引用变化", exact: true })
    .click();
  await expect(panel).toContainText("依赖仍跟随旧版本");
  const save = panel.getByRole("button", {
    name: "保存组合候选，随后独立复核",
    exact: true,
  });
  await expect(save).toBeDisabled();
  await panel
    .getByLabel("将本组内跟随最新的引用改绑到组合版本", { exact: true })
    .check();
  await panel
    .getByRole("button", { name: "预览组合与引用变化", exact: true })
    .click();
  await expect(panel).toContainText("后章组合正文");
  await expect(panel).toContainText("1 处引用将指向组合版本");
  await panel.screenshot({
    path: "../../work/release-composition-preview.png",
  });
  await save.click();
  await expect(panel.getByRole("alert")).toContainText("保存回复中断");
  await save.click();
  await expect(panel.getByRole("status")).toContainText("组合候选已保存");
  expect(requests).toHaveLength(2);
  expect(requests[1]).toEqual(requests[0]);
});
