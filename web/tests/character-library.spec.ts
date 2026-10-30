import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";
import { openIPSection } from "./ip-edit-helper";

test("角色卡按身份检索，关系随时间变化，直接编辑持久保存但不发布", async ({
  page,
}) => {
  await loginAsOwner(page);
  const headers = { Origin: "http://127.0.0.1:18865" };
  const created = await page.request.post("/api/projects", {
    headers,
    data: {
      name: `角色可视化 ${Date.now()}`,
      operation_id: crypto.randomUUID(),
    },
  });
  expect(created.status()).toBe(201);
  const project = await created.json();
  const root = `/api/projects/${project.id}/branches`;
  const branchResponse = await page.request.post(root, {
    headers,
    data: { name: "正史", operation_id: crypto.randomUUID() },
  });
  expect(branchResponse.status()).toBe(201);
  const branch = await branchResponse.json();
  // This UI server predates image endpoints; image service behavior is covered
  // by the dedicated real-MySQL integration test, not this relationship test.
  await page.route(
    `**${root}/${branch.id}/character-images**`,
    async (route) => {
      if (new URL(route.request().url()).pathname.endsWith("/character-images"))
        return route.fulfill({ json: { items: [], next: "" } });
      return route.fulfill({ status: 404 });
    },
  );
  const fact = (id: string, predicate: string, value: string, extra = {}) => ({
    id,
    subject: "hero",
    predicate,
    value,
    kind: "fact",
    effective_from: 0,
    known_from: 0,
    known_by: [],
    visibility: "public",
    recorded_at: "",
    locked: false,
    source: "作者输入",
    ...extra,
  });
  const snapshot = {
    entities: [
      {
        id: "hero",
        kind: "character",
        name: "林舟",
        aliases: ["青衣客"],
        description: "雾城医者",
      },
      {
        id: "other",
        kind: "character",
        name: "林舟",
        aliases: [],
        description: "同名旅人",
      },
      {
        id: "master",
        kind: "character",
        name: "顾先生",
        aliases: [],
        description: "山中药师",
      },
      ...Array.from({ length: 13 }, (_, index) => ({
        id: `peer-${index}`,
        kind: "character",
        name: `同门${index}`,
        aliases: [],
        description: "药师会成员",
      })),
    ],
    assertions: [
      fact("job", "职业", "医师"),
      fact("faction", "阵营", "药师会"),
      fact("tags", "tags", "嘴硬心软，成长;群像"),
      fact("skill", "技能", "辨识百草"),
      fact("age", "年龄", "二十有三"),
      fact("role", "角色定位", "主角"),
      fact("future-faction", "阵营", "未来组织", { effective_from: 10 }),
      fact("rumor-faction", "阵营", "传闻组织", { context: "rumor" }),
      ...Array.from({ length: 13 }, (_, index) =>
        fact(`peer-edge-${index}`, "同门", "共同学医", {
          object_id: `peer-${index}`,
        }),
      ),
      fact("mentor", "师承", "拜顾先生为师", {
        object_id: "master",
        effective_until: 5,
      }),
      fact("rival", "敌对", "后来决裂", {
        object_id: "master",
        effective_from: 5,
      }),
      fact("rumor", "职业", "密探", { context: "rumor" }),
    ],
  };
  const seeded = await page.request.put(`${root}/${branch.id}/draft`, {
    headers,
    data: {
      revision: 0,
      base_head: "",
      snapshot,
      amendment: false,
      amendment_reason: "",
      change_reasons: {},
    },
  });
  expect(seeded.status(), await seeded.text()).toBe(200);
  await page.reload();
  await page.getByRole("button", { name: project.name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  const library = page.getByRole("region", { name: "角色库" });
  await expect(library.getByTestId("character-card")).toHaveCount(16);
  await library.locator(".library-filter-panel > summary").click();
  await library.getByLabel("阵营筛选", { exact: true }).selectOption("药师会");
  await library.getByLabel("职业筛选", { exact: true }).selectOption("医师");
  await library.getByLabel("标签筛选", { exact: true }).selectOption("群像");
  await expect(library.getByTestId("character-card")).toHaveCount(1);
  await expect(library.getByTestId("character-card")).toContainText("辨识百草");
  await expect(library.getByTestId("character-card")).toContainText("二十有三");
  await expect(library.getByTestId("character-card")).toContainText("主角");
  await expect(
    library.getByLabel("阵营筛选", { exact: true }),
  ).not.toContainText("未来组织");
  await expect(
    library.getByLabel("阵营筛选", { exact: true }),
  ).not.toContainText("传闻组织");
  await library
    .getByRole("button", { name: "清除角色筛选", exact: true })
    .click();
  const search = library.getByLabel("搜索角色");
  await search.fill("林舟");
  await expect(library.getByTestId("character-card")).toHaveCount(2);
  await search.fill("青衣客 医师");
  await expect(library.getByTestId("character-card")).toHaveCount(1);
  await library
    .getByRole("button", { name: "打开角色 林舟 · hero", exact: true })
    .click();
  await library.getByRole("button", { name: "补充服饰", exact: true }).click();
  const newField = library.getByLabel("新增角色属性", { exact: true });
  const newValue = library.getByLabel("新增属性内容", { exact: true });
  await expect(newField).toHaveValue("服饰");
  await expect(newValue).toBeFocused();
  await newValue.fill("青布长衫，袖口有药草刺绣");
  await library.getByRole("button", { name: "补充性格", exact: true }).click();
  await expect(newField).toHaveValue("服饰");
  await expect(newValue).toHaveValue("青布长衫，袖口有药草刺绣");
  await expect(library.getByRole("alert")).toContainText("先加入草稿或清空");
  await library
    .getByRole("button", { name: "加入角色草稿", exact: true })
    .click();
  await expect(library.getByTestId("character-card")).toContainText("青布长衫");
  await library
    .getByRole("navigation", { name: "角色详情分区" })
    .getByRole("button", { name: "人物关系", exact: true })
    .click();
  const graph = library.getByRole("group", { name: "当前角色关系图" });
  await expect(graph).toContainText("师承");
  await expect(
    graph.getByRole("button", { name: "查看角色 顾先生 · master" }),
  ).toBeVisible();
  await expect(graph.getByRole("button")).toHaveCount(13);
  await library
    .getByRole("button", { name: "下一组关联角色", exact: true })
    .click();
  await expect(graph.getByRole("button")).toHaveCount(3);
  await expect(
    graph.getByRole("button", {
      name: "查看角色 同门12 · peer-12",
      exact: true,
    }),
  ).toBeVisible();
  await library
    .getByLabel("关系类型筛选", { exact: true })
    .selectOption("师承");
  await expect(graph.getByRole("button")).toHaveCount(2);
  await expect(graph).not.toContainText("同门");
  const originalWidth = (await graph.boundingBox())!.width;
  await library
    .getByRole("button", { name: "放大关系图", exact: true })
    .click();
  expect((await graph.boundingBox())!.width).toBeGreaterThan(originalWidth);
  await library
    .getByRole("button", { name: "重置关系图缩放", exact: true })
    .click();
  await library.getByLabel("关系类型筛选", { exact: true }).selectOption("");
  await expect(library.getByTestId("character-card")).not.toContainText("密探");
  await library.getByLabel("关系与属性的故事位置").fill("5");
  await expect(graph).toContainText("敌对");
  await expect(graph).not.toContainText("师承");
  await graph.getByRole("button", { name: "查看角色 顾先生 · master" }).click();
  await expect(library.getByLabel("角色姓名", { exact: true })).toHaveValue(
    "顾先生",
  );
  await library.getByLabel("角色姓名", { exact: true }).fill("顾青山");
  await library.getByLabel("新增角色属性").fill("性格");
  await library.getByLabel("新增属性内容").fill("严厉但护短");
  await library
    .getByRole("navigation", { name: "角色详情分区" })
    .getByRole("button", { name: "人物关系", exact: true })
    .click();
  await graph.getByRole("button", { name: "查看角色 林舟 · hero" }).click();
  await expect(library.getByRole("alert")).toContainText(
    "请先保存当前属性或图片，或清空未保存的输入，再切换角色。",
  );
  await library
    .getByRole("navigation", { name: "角色详情分区" })
    .getByRole("button", { name: "基础与属性", exact: true })
    .click();
  await expect(library.getByLabel("角色姓名", { exact: true })).toHaveValue(
    "顾青山",
  );
  await expect(library.getByLabel("新增属性内容")).toHaveValue("严厉但护短");
  await library
    .getByRole("button", { name: "加入角色草稿", exact: true })
    .click();
  await expect(library.getByRole("alert")).toHaveCount(0);
  await library.getByLabel("新增角色属性", { exact: true }).fill("惯用动作");
  await library.getByLabel("新增属性内容").fill("思考时转动药杵");
  await library
    .getByRole("button", { name: "加入角色草稿", exact: true })
    .click();
  await expect(library.getByRole("region", { name: "角色详情" })).toContainText(
    "惯用动作",
  );
  await expect
    .poll(async () => {
      const draft = await (
        await page.request.get(`${root}/${branch.id}/draft`)
      ).json();
      return draft.snapshot.entities.find(
        (e: { id: string }) => e.id === "master",
      )?.name;
    })
    .toBe("顾青山");
  await expect
    .poll(async () => {
      const draft = await (
        await page.request.get(`${root}/${branch.id}/draft`)
      ).json();
      return draft.snapshot.assertions.some(
        (a: { subject: string; value: string }) =>
          a.subject === "master" && a.value === "严厉但护短",
      );
    })
    .toBe(true);
  const official = await (
    await page.request.get(`${root}/${branch.id}`)
  ).json();
  expect(official.head).toBe("");
  expect(official.snapshot.entities ?? []).toHaveLength(0);
  await search.fill("");
  await library.screenshot({
    path: "../../work/character-library-preview.png",
  });
  await search.fill("严厉但护短");
  await expect(library.getByTestId("character-card")).toHaveCount(1);
  await page.reload();
  await page.getByRole("button", { name: project.name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await expect(library).toContainText("顾青山");
  await library.getByLabel("搜索角色").fill("hero");
  await expect(library.getByTestId("character-card")).toContainText("青布长衫");
  // Only this locked-authority view is mocked; draft persistence above uses the real API.
  await page.route(`**${root}/${branch.id}`, (route) =>
    route.fulfill({
      json: {
        ...official,
        snapshot: {
          ...snapshot,
          entities: snapshot.entities.map((e) => ({
            ...e,
            locked: e.id === "master",
          })),
        },
      },
    }),
  );
  await page.reload();
  await page.getByRole("button", { name: project.name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库", exact: true }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await library
    .getByRole("button", { name: "打开角色 顾青山 · master", exact: true })
    .click();
  await expect(library.getByLabel("角色姓名", { exact: true })).toBeDisabled();
  await expect(
    library.getByRole("button", { name: "补充服饰", exact: true }),
  ).toBeDisabled();
  await openIPSection(page, "设定草稿");
  await page.getByLabel("提出锁定设定修订（须负责人专门批准）").check();
  await openIPSection(page, "角色资料");
  await expect(library.getByLabel("角色姓名", { exact: true })).toBeEnabled();
});
