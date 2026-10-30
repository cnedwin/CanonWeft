import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";
import { fileURLToPath } from "node:url";
import { openIPSection, saveReasonedCandidate } from "./ip-edit-helper";

test("真实扫描 PDF 展示 OCR 来源，空白图保留待核对且损坏 PDF 明确报错", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await loginAsOwner(page);
  const name = `扫描资料 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("从文件导入资料", { exact: true }).click();

  const fixture = (name: string) =>
    fileURLToPath(
      new URL(`../../tools/ingest/tests/fixtures/${name}`, import.meta.url),
    );
  await page.getByLabel("上传资料文件").setInputFiles(fixture("scan.pdf"));
  await expect(page.getByLabel("导入内容 · 可修改")).toHaveValue(/星河/, {
    timeout: 30_000,
  });
  const edited =
    (await page.getByLabel("导入内容 · 可修改").inputValue()) +
    "\n作者补充：主角在第二日抵达。";
  await page.getByLabel("导入内容 · 可修改").fill(edited);
  await expect(page.getByTestId("import-draft-status")).toHaveText(
    "导入草稿已自动暂存",
  );
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("从文件导入资料", { exact: true }).click();
  await expect(page.getByLabel("导入内容 · 可修改")).toHaveValue(edited);
  await page.getByText("查看完整覆盖报告", { exact: true }).click();
  await expect(page.locator(".source-import")).toContainText("page:1");
  await expect(page.locator(".source-import")).toContainText("page:2");
  await expect(page.locator(".source-import")).toContainText("最低行置信度");
  await expect(page.locator(".source-import")).toContainText(
    "不代表图像内容理解",
  );
  await expect(
    page.getByRole("button", { name: "保存文件内容候选", exact: true }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "关闭本次导入编辑" }).click();

  await expect(page.getByLabel("上传资料文件")).toBeEnabled();
  await page.getByLabel("上传资料文件").setInputFiles(fixture("blank.png"));
  await expect(page.getByLabel("导入内容 · 可修改")).toHaveValue("", {
    timeout: 30_000,
  });
  await page.getByText("查看完整覆盖报告", { exact: true }).click();
  await expect(page.locator(".source-import")).toContainText("保留为视觉参考");
  const save = page.getByRole("button", {
    name: "保存文件内容候选",
    exact: true,
  });
  await expect(save).toBeDisabled();
  await page
    .getByLabel("导入内容 · 可修改")
    .fill("人工核对：仅作空白视觉参考。");
  await expect(save).toBeDisabled();
  await page
    .getByLabel("我已检查遗漏，只将当前文本保存为候选，保留待核对记录")
    .check();
  await expect(save).toBeEnabled();
  await page.getByRole("button", { name: "关闭本次导入编辑" }).click();

  await expect(page.getByLabel("上传资料文件")).toBeEnabled();
  await page.getByLabel("上传资料文件").setInputFiles({
    name: "broken.pdf",
    mimeType: "application/pdf",
    buffer: Buffer.from("%PDF-broken"),
  });
  await expect(page.getByRole("alert")).toContainText("文件损坏", {
    timeout: 30_000,
  });
});

test("导入草稿读取失败可重试，迟到暂存回执与另一页面修改均不覆盖本页", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await loginAsOwner(page);
  const name = `导入恢复 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("从文件导入资料", { exact: true }).click();
  let failedRead = false;
  let draftPath = "";
  await page.route("**/imports/*/draft", async (route) => {
    if (route.request().method() === "GET" && !failedRead) {
      failedRead = true;
      draftPath = new URL(route.request().url()).pathname;
      await route.fulfill({ status: 503, json: { error: "测试读取中断" } });
    } else await route.continue();
  });
  await page.getByLabel("上传资料文件").setInputFiles({
    name: "故事设定.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("主角出发寻找旧城。"),
  });
  await expect(page.getByRole("alert")).toContainText("测试读取中断", {
    timeout: 15_000,
  });
  await page
    .getByRole("button", { name: "重试读取导入草稿", exact: true })
    .click();
  const editor = page.getByLabel("导入内容 · 可修改");
  await expect(editor).toHaveValue("主角出发寻找旧城。");
  await expect(editor).toBeEnabled();
  await page.unroute("**/imports/*/draft");
  let releaseReply!: () => void;
  const gate = new Promise<void>((resolve) => {
    releaseReply = resolve;
  });
  let committed!: () => void;
  const receipt = new Promise<void>((resolve) => {
    committed = resolve;
  });
  let first = true;
  await page.route(`**${draftPath}`, async (route) => {
    if (route.request().method() !== "PUT" || !first) {
      await route.continue();
      return;
    }
    first = false;
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    committed();
    await gate;
    await route.fulfill({ response });
  });
  await editor.fill("第一次修改。");
  await receipt;
  await editor.fill("等待回执时继续修改🌙。");
  releaseReply();
  await expect(page.getByTestId("import-draft-status")).toHaveText(
    "导入草稿已自动暂存",
  );
  await expect(editor).toHaveValue("等待回执时继续修改🌙。");
  expect((await (await page.request.get(draftPath)).json()).content.text).toBe(
    "等待回执时继续修改🌙。",
  );
  const second = await page.context().newPage();
  await second.goto("/");
  await second.getByRole("button", { name, exact: true }).click();
  await second.getByRole("button", { name: "打开设定库" }).click();
  await second.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(second, "版本与分支");
  await second.getByText("从文件导入资料", { exact: true }).click();
  await expect(second.getByLabel("导入内容 · 可修改")).toHaveValue(
    "等待回执时继续修改🌙。",
  );
  await second.getByLabel("导入内容 · 可修改").fill("第二页面的新故事。");
  await expect(second.getByTestId("import-draft-status")).toHaveText(
    "导入草稿已自动暂存",
  );
  await editor.fill("第一页面保留的不同故事。");
  await expect(
    page.getByText(
      "另一页面保存了新稿，本页修改仍保留。请先复制需要保留的内容，再读取最新草稿。",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(editor).toHaveValue("第一页面保留的不同故事。");
  expect((await (await page.request.get(draftPath)).json()).content.text).toBe(
    "第二页面的新故事。",
  );
  await page
    .getByRole("button", {
      name: "放弃本页修改，读取最新导入草稿",
      exact: true,
    })
    .click();
  await expect(editor).toHaveValue("第二页面的新故事。");
  await second.close();
});

test("导入旧草稿必须核对新设定后继续，保留作者文字与原始来源", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await loginAsOwner(page);
  const name = `导入版本核对 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("从文件导入资料", { exact: true }).click();
  const uploadReceipt = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/imports/jobs"),
  );
  await page.getByLabel("上传资料文件").setInputFiles({
    name: "港口资料.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("原始记录：旧港口。"),
  });
  const editor = page.getByLabel("导入内容 · 可修改");
  await expect(editor).toHaveValue("原始记录：旧港口。", { timeout: 15_000 });
  const uploaded = await uploadReceipt;
  const sourcePath =
    new URL(uploaded.url()).pathname.slice(0, -"/jobs".length) +
    "/" +
    (await uploaded.json()).id;
  const originalSource = await (await page.request.get(sourcePath)).json();
  await editor.fill("作者核实：林舟清晨抵达旧港口。");
  await expect(page.getByTestId("import-draft-status")).toHaveText(
    "导入草稿已自动暂存",
  );
  const second = await page.context().newPage();
  await second.goto("/");
  await second.getByRole("button", { name, exact: true }).click();
  await second.getByRole("button", { name: "打开设定库" }).click();
  await second.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(second, "设定草稿");
  await second.getByRole("button", { name: "新增角色或实体" }).click();
  await second.getByLabel("实体名称", { exact: true }).fill("林舟");
  await second.getByLabel("实体简介").fill("在港口行医的青年。");
  await saveReasonedCandidate(second);
  await second.getByRole("button", { name: "确认采用此候选" }).first().click();
  await second.getByRole("button", { name: "发布此版本" }).first().click();
  await expect(second.getByTestId("published-snapshot")).toContainText("林舟");
  await second.close();
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(page, "版本与分支");
  await page.getByText("从文件导入资料", { exact: true }).click();
  await expect(editor).toHaveValue("作者核实：林舟清晨抵达旧港口。");
  const save = page.getByRole("button", {
    name: "保存文件内容候选",
    exact: true,
  });
  await expect(save).toBeDisabled();
  await page
    .getByRole("button", { name: "查看最新设定并核对", exact: true })
    .click();
  await expect(page.getByTestId("import-current-setting")).toContainText(
    "林舟",
  );
  await expect(page.getByTestId("import-current-setting")).toContainText(
    "在港口行医的青年。",
  );
  await expect(save).toBeDisabled();
  const third = await page.context().newPage();
  await third.goto("/");
  await third.getByRole("button", { name, exact: true }).click();
  await third.getByRole("button", { name: "打开设定库" }).click();
  await third.getByRole("button", { name: "正史", exact: true }).click();
  await openIPSection(third, "设定草稿");
  await third.getByRole("button", { name: "新增角色或实体" }).click();
  await third.getByLabel("实体名称", { exact: true }).last().fill("望海亭");
  await third.getByLabel("实体简介").last().fill("港口的报时亭。");
  await saveReasonedCandidate(third);
  await third.getByRole("button", { name: "确认采用此候选" }).first().click();
  await third.getByRole("button", { name: "发布此版本" }).first().click();
  await expect(third.getByTestId("published-snapshot")).toContainText("望海亭");
  await third.close();
  await page
    .getByRole("button", { name: "已核对，以此版本继续编辑", exact: true })
    .click();
  await expect(page.locator(".source-import")).toContainText(
    "核对期间设定又有更新",
  );
  await expect(save).toBeDisabled();
  await page
    .getByRole("button", { name: "查看最新设定并核对", exact: true })
    .click();
  await expect(page.getByTestId("import-current-setting")).toContainText(
    "望海亭",
  );
  await page
    .getByRole("button", { name: "已核对，以此版本继续编辑", exact: true })
    .click();
  await expect(page.getByTestId("import-draft-status")).toHaveText(
    "导入草稿已自动暂存",
  );
  await expect(editor).toHaveValue("作者核实：林舟清晨抵达旧港口。");
  await expect(save).toBeEnabled();
  await save.click();
  await expect(page.locator(".source-import")).toContainText(
    "文件内容已保存为候选",
  );
  const retainedSource = await (await page.request.get(sourcePath)).json();
  expect(retainedSource.hash).toBe(originalSource.hash);
  expect(retainedSource.report.text).toBe("原始记录：旧港口。");
});
