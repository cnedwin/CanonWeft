import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";

test("角色图片保留上传重试，按造型提示缺图，刷新后仍能预览", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  const projectResponse = await page.request.post("/api/projects", {
    headers,
    data: { name: `角色视觉 ${Date.now()}`, operation_id: crypto.randomUUID() },
  });
  expect(projectResponse.status()).toBe(201);
  const project = await projectResponse.json();
  const branchResponse = await page.request.post(
    `/api/projects/${project.id}/branches`,
    { headers, data: { name: "小说正史", operation_id: crypto.randomUUID() } },
  );
  expect(branchResponse.status()).toBe(201);
  const branch = await branchResponse.json();
  const base = `/api/projects/${project.id}/branches/${branch.id}`;
  const seeded = await page.request.put(`${base}/draft`, {
    headers,
    data: {
      revision: 0,
      base_head: "",
      snapshot: {
        entities: [
          {
            id: "hero",
            kind: "character",
            name: "林舟",
            aliases: [],
            description: "青衣医者",
          },
          {
            id: "friend",
            kind: "character",
            name: "故人",
            aliases: [],
            description: "旧友",
          },
        ],
        assertions: [],
      },
      amendment: false,
      amendment_reason: "",
    },
  });
  expect(seeded.status()).toBe(200);
  const png = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jT1kAAAAASUVORK5CYII=",
    "base64",
  );
  const item = {
    id: "saved-image",
    name: "正面.png",
    hash: "a".repeat(64),
    published: false,
    report: {
      character_image: {
        character_id: "hero",
        look: "日常青衣",
        view: "front",
        description: "青衣，腰间药囊",
        source: "本人绘制",
        rights: "本人原创",
        mime: "image/png",
        width: 1,
        height: 1,
      },
    },
  };
  let saved = false;
  let references: (typeof item)[] = [];
  const attempts: { key: string | undefined; body: string }[] = [];
  await page.route(`**${base}/character-images**`, async (route) => {
    const request = route.request();
    if (request.url().includes("/file"))
      return route.fulfill({ contentType: "image/png", body: png });
    if (request.method() === "GET")
      return route.fulfill({
        json: { items: saved ? [item, ...references] : [], next: "" },
      });
    attempts.push({
      key: request.headers()["idempotency-key"],
      body: request.postData() ?? "",
    });
    if (attempts.length === 1)
      return route.fulfill({
        status: 500,
        json: { error: "回复中断，请保留输入重试" },
      });
    saved = true;
    return route.fulfill({ status: 201, json: item });
  });
  async function openLibrary() {
    await page.reload();
    await page.getByRole("button", { name: project.name, exact: true }).click();
    await page.getByRole("button", { name: "打开设定库", exact: true }).click();
    await page.getByRole("button", { name: "小说正史", exact: true }).click();
    await page
      .getByRole("button", { name: "打开角色 林舟 · hero", exact: true })
      .click();
    await page
      .getByRole("navigation", { name: "角色详情分区" })
      .getByRole("button", { name: "图片与三视图", exact: true })
      .click();
  }
  await openLibrary();
  const gallery = page.getByRole("region", { name: "角色视觉资料" });
  await expect(gallery.getByText("尚无图片", { exact: false })).toBeVisible();
  await gallery.getByLabel("造型名称", { exact: true }).fill("日常青衣");
  await gallery
    .getByLabel("图片文件", { exact: true })
    .setInputFiles({ name: "正面.png", mimeType: "image/png", buffer: png });
  await gallery.getByLabel("造型说明", { exact: true }).fill("青衣，腰间药囊");
  await gallery.getByLabel("图片来源", { exact: true }).fill("本人绘制");
  await gallery.getByLabel("使用权限说明", { exact: true }).fill("本人原创");
  await page
    .getByRole("button", { name: "打开角色 故人 · friend", exact: true })
    .click();
  await expect(
    page.getByText("请先保存当前属性或图片，或清空未保存的输入，再切换角色。", {
      exact: true,
    }),
  ).toBeVisible();
  await gallery
    .getByRole("button", { name: "上传并保存待采用参考", exact: true })
    .click();
  await expect(gallery.getByRole("alert")).toContainText("回复中断");
  await expect(gallery.getByLabel("造型说明", { exact: true })).toHaveValue(
    "青衣，腰间药囊",
  );
  await gallery
    .getByRole("button", { name: "上传并保存待采用参考", exact: true })
    .click();
  await expect(gallery.getByText("待采用", { exact: true })).toBeVisible();
  expect(attempts).toHaveLength(2);
  expect(attempts[0]!.key).toBe(attempts[1]!.key);
  for (const attempt of attempts)
    expect(attempt.body).toContain('"character_id":"hero"');
  await expect(
    gallery.getByText("已采用三视图缺少：正面、侧面、背面", { exact: true }),
  ).toBeVisible();
  await gallery
    .getByRole("button", { name: "放大正面 正面.png", exact: true })
    .click();
  await expect(gallery.getByRole("dialog", { name: "图片大图" })).toBeVisible();
  await gallery.getByRole("button", { name: "关闭大图", exact: true }).click();
  await openLibrary();
  await expect(gallery.getByText("待采用", { exact: true })).toBeVisible();
  await expect(
    gallery.getByRole("img", { name: "日常青衣 · 正面", exact: true }),
  ).toBeVisible();
  const official = await page.request.get(base);
  expect((await official.json()).head).toBe("");
  // Read-only comparisons retain distinct adopted references; other looks and
  // unadopted images cannot fill this costume's missing angles.
  item.published = true;
  references = [
    { ...item, id: "alternate-front", name: "正面旧版.png" },
    {
      ...item,
      id: "pending-side",
      published: false,
      report: {
        character_image: { ...item.report.character_image, view: "side" },
      },
    },
    {
      ...item,
      id: "armor-back",
      name: "盔甲背面.png",
      report: {
        character_image: {
          ...item.report.character_image,
          view: "back",
          look: "战斗盔甲",
        },
      },
    },
  ];
  await openLibrary();
  const comparison = gallery.getByRole("region", { name: "三视图并排对照" });
  await expect(
    comparison.getByText("侧面参考待补充", { exact: true }),
  ).toBeVisible();
  await expect(
    comparison.getByText("背面参考待补充", { exact: true }),
  ).toBeVisible();
  await comparison.getByLabel("正面参考版本").selectOption("alternate-front");
  await comparison
    .getByRole("button", { name: "对照放大正面 正面旧版.png", exact: true })
    .click();
  await expect(
    gallery.getByRole("dialog", { name: "图片大图" }).getByRole("img"),
  ).toHaveAttribute("src", /alternate-front\/file/);
  await gallery.getByRole("button", { name: "关闭大图", exact: true }).click();
  await page
    .getByRole("navigation", { name: "角色详情分区" })
    .getByRole("button", { name: "基础与属性", exact: true })
    .click();
  await expect(
    page.getByRole("region", { name: "阵营与能力", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("navigation", { name: "角色详情分区" })
    .getByRole("button", { name: "图片与三视图", exact: true })
    .click();
  await gallery
    .getByLabel("查看造型", { exact: true })
    .selectOption("战斗盔甲");
  await expect(
    comparison.getByText("正面参考待补充", { exact: true }),
  ).toBeVisible();
  await expect(
    comparison.getByRole("button", {
      name: "对照放大背面 盔甲背面.png",
      exact: true,
    }),
  ).toBeVisible();
  expect(attempts).toHaveLength(2);
  await comparison.screenshot({
    path: "../../work/character-overview-preview.png",
  });
});
