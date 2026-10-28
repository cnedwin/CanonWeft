const { chromium, expect } = require("@playwright/test");
(async () => {
  const [origin, title, phase] = process.argv.slice(2);
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error("isolated loopback required");
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    page.setDefaultTimeout(12000); const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.goto(origin); await page.getByLabel("用户名", {exact:true}).fill("owner"); await page.getByLabel("密码", {exact:true}).fill("test-owner-password-123"); await page.getByRole("button",{name:"登录",exact:true}).click();
    async function enter() {
      await page.getByRole("button",{name:`${title} · 继续完善世界与故事`,exact:true}).click();
      await page.getByRole("button",{name:"题材研究与事实卡",exact:true}).click();
      await page.getByRole("button",{name:/泉州商舶考据 · 第\d+版/}).click();
    }
    if (phase === "withdraw-source") {
      async function material() {
        await page.getByRole("button",{name:`${title} · 继续完善世界与故事`,exact:true}).click();
        await page.getByRole("button",{name:"管理本书资料",exact:true}).click();
        await page.getByRole("button",{name:"编辑 港口原始资料",exact:true}).click();
      }
      await material();
      await page.getByLabel("撤回这份资料的使用",{exact:true}).check();
      await page.getByLabel("撤回原因",{exact:true}).fill("来源结论已撤回，等待可靠的一手资料");
      await expect(page.getByText("草稿已暂存；发布后才成为正式资料",{exact:true})).toBeVisible();
      await page.reload(); await material();
      await expect(page.getByLabel("撤回这份资料的使用",{exact:true})).toBeChecked();
      await expect(page.getByLabel("撤回原因",{exact:true})).toHaveValue("来源结论已撤回，等待可靠的一手资料");
      expect(errors).toEqual([]);
      console.log("Withdrawal editor: real private policy saved/reloaded; formal publication remains separate.");
      return;
    }
    if (phase === "withdraw-review") {
      await page.getByRole("button",{name:`${title} · 继续完善世界与故事`,exact:true}).click();
      await page.getByRole("button",{name:"资料变更与待复核",exact:true}).click();
      const queue=page.getByRole("region",{name:"资料变更与待复核",exact:true});
      await expect(queue.getByRole("article",{name:"港口原始资料",exact:true})).toContainText("已撤回使用");
      const item=queue.getByRole("article",{name:"泉州商舶考据",exact:true});
      await expect(item).toContainText("已撤回使用，请刷新来源");
      await item.getByRole("button",{name:"打开这份研究稿",exact:true}).click();
      await expect(page.getByLabel("研究标题",{exact:true})).toHaveValue("泉州商舶考据");
      await page.getByRole("button",{name:"刷新来源并标记待核对",exact:true}).click();
      await expect(page.getByRole("region",{name:"已撤回的研究来源",exact:true})).toContainText("来源结论已撤回，等待可靠的一手资料");
      const c=page.getByRole("article",{name:"事实卡 2",exact:true});
      await expect(c.getByLabel("我已核对，选择作为写作参考",{exact:true})).not.toBeChecked();
      await expect(c.locator("blockquote")).toHaveCount(2);
      await page.reload(); await enter();
      await expect(page.getByRole("region",{name:"已撤回的研究来源",exact:true})).toContainText("港口原始资料");
      if(process.env.STUDIO_RESEARCH_FACTS_SCREENSHOT) await page.screenshot({path:process.env.STUDIO_RESEARCH_FACTS_SCREENSHOT.replace(/\.png$/,"-withdraw.png"),fullPage:true});
      expect(errors).toEqual([]);
      console.log("Withdrawal queue: source and private notebook visible, direct editor routing, exact historical citations and author text survive refresh/reload.");
      return;
    }
    await enter();
    const card = page.getByRole("article",{name:"事实卡 1",exact:true});
    if (phase === "refresh") {
      await page.getByRole("button",{name:"刷新来源并标记待核对",exact:true}).click();
      await expect(page.getByText("来源版本已核对；受影响卡片已取消选择，旧版引证保留待复核。正式资料和章节未改动。",{exact:true})).toBeVisible();
      await expect(card.getByLabel("我已核对，选择作为写作参考",{exact:true})).not.toBeChecked();
      await expect(card.getByLabel("我已核对，选择作为写作参考",{exact:true})).toBeDisabled();
      await expect(card.locator("blockquote")).toHaveText("市舶机构办理商舶事务");
      await expect(card.getByLabel("写作用法",{exact:true})).toHaveValue("以港口登记冲突带出主角身份，不讲解百科。");
      await page.reload(); await enter();
      await expect(card.locator("blockquote")).toHaveText("市舶机构办理商舶事务");
      await expect(card.getByRole("combobox",{name:/证据状态/})).toHaveValue("unverified");
      if (process.env.STUDIO_RESEARCH_FACTS_SCREENSHOT) await page.screenshot({path:process.env.STUDIO_RESEARCH_FACTS_SCREENSHOT.replace(/\.png$/, "-refresh.png"),fullPage:true});
      if (errors.length) throw new Error(errors.join("\n"));
      console.log("Source refresh UI: changed quotations retained, affected selection revoked, author edits preserved, and refresh state persisted.");
      return;
    }
    await expect(card.getByLabel("单项结论",{exact:true})).toHaveValue("存在办理商舶事务的机构");
    await expect(card.getByLabel("我已核对，选择作为写作参考",{exact:true})).not.toBeChecked();
    await card.getByRole("button",{name:"查看引用原文",exact:true}).click();
    await expect(page.locator("pre")).toContainText("市舶机构办理商舶事务");
    await card.getByLabel("写作用法",{exact:true}).fill("以港口登记冲突带出主角身份，不讲解百科。");
    await card.getByLabel("我已核对，选择作为写作参考",{exact:true}).check();
    await expect(page.getByRole("button",{name:"返回小说工作台",exact:true})).toBeDisabled();
    await page.getByRole("button",{name:"保存事实卡修改",exact:true}).click();
    await expect(page.getByText("研究稿已保存",{exact:true})).toBeVisible();
    await page.reload(); await enter();
    await expect(card.getByLabel("写作用法",{exact:true})).toHaveValue("以港口登记冲突带出主角身份，不讲解百科。");
    await expect(card.getByLabel("我已核对，选择作为写作参考",{exact:true})).toBeChecked();
    await page.getByText("建议检索计划",{exact:true}).click();
    await expect(page.getByLabel("检索词",{exact:true})).toHaveValue("宋代 市舶司 流程");
    await page.getByRole("button",{name:"将选中卡片整理为参考资料候选",exact:true}).click();
    await expect(page.getByText("已生成参考资料候选；模型来源仍需独立审查，通过后由作者确认采用和发布。",{exact:true})).toBeVisible();
    await expect(page.getByRole("button",{name:"查看候选并进行独立审查",exact:true})).toBeVisible();
    if (process.env.STUDIO_RESEARCH_FACTS_SCREENSHOT) await page.screenshot({path:process.env.STUDIO_RESEARCH_FACTS_SCREENSHOT,fullPage:true});
    await page.getByRole("button",{name:"让研究助手分析资料",exact:true}).click();
    await page.getByRole("button",{name:"保存请求并估算费用",exact:true}).click();
    await expect(page.getByText("要求、资料和方法版本已保存，尚未调用模型。",{exact:true})).toBeVisible();
    if (errors.length) throw new Error(errors.join("\n"));
    console.log("Research facts UI: real stored Agent proposal, source text, edits/approval across reload, search plan, review-required candidate, and prepared research task without paid call.");
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exit(1); });
