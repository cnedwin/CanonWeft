import { loginAsOwner } from "./owner-session";
import { saveReasonedCandidate } from "./ip-edit-helper";
import { test, expect } from "@playwright/test";

test("价格版本与生成估算可保存，付费关闭时执行被拒绝", async ({ page }) => {
  await loginAsOwner(page);
  await page.getByRole("button", { name: "模型与用途设置" }).click();
  await page
    .getByRole("button", { name: "添加 DeepSeek", exact: true })
    .click();
  const config = `生成门禁 ${Date.now()}`;
  await page.getByLabel("配置名称", { exact: true }).fill(config);
  await page
    .getByLabel("API Key", { exact: true })
    .fill("test-only-browser-key-not-real");
  await page.getByRole("button", { name: "保存模型配置", exact: true }).click();
  await page
    .getByLabel("使用的模型", { exact: true })
    .selectOption({ label: `${config} · deepseek-flash` });
  await page.getByRole("button", { name: "保存用途绑定" }).click();
  await page
    .getByLabel("专业用途", { exact: false })
    .selectOption("continuity_editor");
  await page.getByRole("button", { name: "保存用途绑定" }).click();
  await page.getByLabel("允许预算范围内的付费调用").uncheck();
  await page.getByRole("button", { name: "保存预算设置" }).click();
  await expect(
    page.getByText("预算已保存，付费调用保持关闭。", { exact: true }),
  ).toBeVisible();
  await page
    .getByLabel("价格对应配置")
    .selectOption({ label: `${config} · deepseek-flash · 版本 1` });
  await page.getByLabel("输入价格 / 百万 token", { exact: true }).fill("1");
  await page.getByLabel("输出价格 / 百万 token", { exact: true }).fill("2");
  await page
    .getByLabel("缓存读取价格 / 百万 token", { exact: true })
    .fill("0.1");
  await page.getByLabel("缓存写入价格 / 百万 token", { exact: true }).fill("1");
  await page
    .getByLabel("官方价格来源")
    .fill("https://api-docs.deepseek.com/quick_start/pricing/");
  await page.getByRole("button", { name: "保存价格版本" }).click();
  await expect(
    page.getByText("价格版本已保存，未发起任何模型调用。"),
  ).toBeVisible();
  await page.reload();
  const name = `生成任务门禁 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page
    .getByRole("button", { name: "Skill 研究与编辑", exact: true })
    .click();
  const capabilities = page.locator(".capability-panel");
  await capabilities
    .getByRole("button", { name: "新增专业角色", exact: true })
    .click();
  await capabilities
    .getByLabel("能力名称", { exact: true })
    .fill("创作方法试评");
  await capabilities.getByLabel("稳定标识").fill("writer-trial");
  await capabilities.getByLabel("适用任务").fill("验证角色清单固定和费用开关");
  await capabilities
    .getByLabel("职责与执行方法")
    .fill("根据材料提出候选，不能自授资格。输出 summary 和 issues。");
  await capabilities
    .getByLabel("模型用途", { exact: true })
    .selectOption("writer");
  await capabilities.getByText("高级输入输出契约", { exact: true }).click();
  await capabilities.getByLabel("输入契约", { exact: true }).fill(
    JSON.stringify({
      type: "object",
      allOf: [
        { properties: { source: { type: "string" } }, required: ["source"] },
      ],
    }),
  );
  await capabilities.getByRole("button", { name: "保存为新能力版本" }).click();
  await expect(capabilities.getByText(/可进入受控文本试评/)).toBeVisible();
  await page.getByRole("button", { name: "返回项目书架" }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByRole("button", { name: "构建 / 更新检索索引" }).click();
  await expect(page.getByText("索引已就绪，共 0 个原文片段。")).toBeVisible();
  await page.getByLabel("要查找的线索").fill("规划世界的基础设定");
  await page.getByRole("button", { name: "查找并保存证据包" }).click();
  await page.getByRole("button", { name: "用于下方 AI 候选任务" }).click();
  await page.getByLabel("候选用途", { exact: true }).selectOption("writer");
  await page
    .getByLabel("固定角色版本", { exact: true })
    .selectOption({ index: 1 });
  await page
    .getByLabel("完整试评输入", { exact: true })
    .fill('{"source":"复杂契约测试，无付费授权。"}');
  await page
    .getByLabel("希望 AI 协助的内容")
    .fill("这是门禁验证请求，不得发往真实模型。");
  await page.getByRole("button", { name: "保存请求并估算费用" }).click();
  await expect(page.getByRole("alert")).toContainText(
    "证据包用途与候选用途不匹配",
  );
  await page.getByRole("button", { name: "移除关联证据包" }).click();
  await page.getByRole("button", { name: "保存请求并估算费用" }).click();
  await expect(
    page.getByText("候选任务与费用估算已保存，尚未发送模型请求。"),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "执行此请求（需要开启付费预算）" })
    .click();
  await expect(
    page.getByText(
      "已固定专业角色及完整方法清单；本次是能力试评，不能作为生产资格证明。",
    ),
  ).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("付费调用尚未开启");
  await page.getByRole("button", { name: "取消任务", exact: true }).click();
  await expect(page.locator(".generation-task")).toContainText("已取消");
  await page.getByRole("button", { name: "新增角色或实体" }).click();
  await page.getByLabel("实体名称", { exact: true }).fill("林舟");
  await page.getByLabel("实体简介").fill("需要审查的新角色设定");
  await expect(page.getByText("草稿已自动暂存，尚未采用")).toBeVisible();
  await saveReasonedCandidate(page);
  await page.getByText("依赖、冻结与变更影响", { exact: true }).click();
  await page.getByLabel("待分析的设定候选").selectOption({ index: 1 });
  await page
    .getByRole("button", { name: "生成变更影响清单", exact: true })
    .click();
  await page
    .getByRole("button", { name: "用于 AI 审查或连续性修订", exact: true })
    .click();
  await expect(page.getByLabel("候选用途", { exact: true })).toHaveValue(
    "continuity_editor",
  );
  await expect(
    page
      .getByLabel("候选用途", { exact: true })
      .locator('option[value="writer"]'),
  ).toHaveCount(0);
  await page
    .getByLabel("希望 AI 协助的内容")
    .fill("检查新增角色设定的影响，只保存请求，不执行模型调用。");
  await page
    .getByRole("button", { name: "保存请求并估算费用", exact: true })
    .click();
  await expect(
    page.getByText("已冻结变更影响清单；过期后不会发送或复用本任务的结果。"),
  ).toBeVisible();
  await page
    .getByRole("button", {
      name: "执行此请求（需要开启付费预算）",
      exact: true,
    })
    .click();
  await expect(page.getByRole("alert")).toContainText("付费调用尚未开启");
  await page.getByRole("button", { name: "取消任务", exact: true }).click();
  await expect(page.locator(".generation-task")).toHaveCount(2);
});
