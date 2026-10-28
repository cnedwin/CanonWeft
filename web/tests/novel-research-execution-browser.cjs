const {chromium, expect} = require('@playwright/test');
(async () => {
  const [origin, title] = process.argv.slice(2);
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error('isolated loopback required');
  const browser = await chromium.launch();
  try {
    const context = await browser.newContext({viewport:{width:1440,height:1000}});
    let page = await context.newPage();
    const errors = [];
    function observe() {
      page.setDefaultTimeout(15000);
      page.on('pageerror', e => errors.push(e.message));
    }
    observe();
    await page.goto(origin);
    await page.getByLabel('用户名',{exact:true}).fill('owner');
    await page.getByLabel('密码',{exact:true}).fill('test-owner-password-123');
    await page.getByRole('button',{name:'登录',exact:true}).click();
    async function enter() {
      await page.getByRole('button',{name:`${title} · 继续完善世界与故事`,exact:true}).click();
      await page.getByRole('button',{name:'题材研究与事实卡',exact:true}).click();
      await page.getByRole('button',{name:/^港口制度 · 第\d+版/}).click();
    }
    await enter();
    let plan = page.getByRole('region',{name:'执行检索计划',exact:true});
    async function reopen() {
      page = await context.newPage(); observe();
      await page.goto(origin); await enter();
      plan = page.getByRole('region',{name:'执行检索计划',exact:true});
    }
    async function withoutPage(runURL, stage) {
      await page.close();
      await expect.poll(async () => {
        const response = await context.request.get(runURL,{headers:{Origin:origin}});
        if (!response.ok()) throw new Error(`run read: ${response.status()}`);
        const run = await response.json();
        if (run.document.error) throw new Error(run.document.error);
        return run.document.stage;
      },{timeout:20000}).toBe(stage);
      await reopen();
    }
    await plan.getByRole('combobox',{name:/新任务采集方式/}).selectOption('0');
    await plan.getByRole('button',{name:'建立本版检索任务',exact:true}).click();
    await expect(plan.getByRole('status')).toContainText('待继续检索');
    await plan.getByRole('button',{name:'暂停当前任务',exact:true}).click();
    await expect(plan.getByRole('status')).toContainText('已暂停');
    await page.reload(); await enter();
    await expect(plan.getByRole('status')).toContainText('已暂停');
    const queued = page.waitForResponse(r => /\/runs\/[^/]+\/background$/.test(r.url()) && r.request().method()==='POST' && r.status()===200);
    await plan.getByRole('button',{name:'继续检索或采集',exact:true}).click();
    const started = await queued;
    const runURL = started.url().replace(/\/background$/,'');
    await withoutPage(runURL,'selection');
    await expect(plan.getByRole('status')).toContainText('检索完成，请选择资料', {timeout:20000});
    let checks = plan.getByRole('checkbox',{name:'选择 市舶司',exact:true});
    await expect(checks).toHaveCount(2);
    await checks.nth(0).check(); await checks.nth(1).check();
    await expect(page.getByRole('button',{name:'返回小说工作台',exact:true})).toBeDisabled();
    const selection = page.waitForResponse(r => /\/runs\/[^/]+\/selection$/.test(r.url()) && r.request().method()==='PUT' && r.status()===200);
    await plan.getByRole('button',{name:'保存资料选择',exact:true}).click();
    await selection;
    await withoutPage(runURL,'complete');
    checks = plan.getByRole('checkbox',{name:'选择 市舶司',exact:true});
    await expect(checks.nth(0)).toBeChecked(); await expect(checks.nth(1)).toBeChecked();
    await expect(plan.getByRole('status')).toContainText('选中资料已采集');
    await plan.getByRole('button',{name:'编辑采集稿：市舶司',exact:true}).first().click();
    const editor = plan.getByLabel('导入内容 · 可修改',{exact:true});
    await expect(editor).toHaveValue(/oldid=71/);
    const saved = page.waitForResponse(r => /\/imports\/[^/]+\/draft$/.test(r.url()) && r.request().method()==='PUT' && r.status()===200);
    await editor.fill((await editor.inputValue())+'\n研究任务备注：核对年代后使用。');
    await saved;
    await expect(plan.getByText('导入草稿已自动暂存',{exact:true})).toBeVisible();
    await page.reload(); await enter();
    await plan.getByRole('button',{name:'编辑采集稿：市舶司',exact:true}).first().click();
    await expect(editor).toHaveValue(/研究任务备注：核对年代后使用。/);
    if (process.env.STUDIO_RESEARCH_EXECUTION_SCREENSHOT) await page.screenshot({path:process.env.STUDIO_RESEARCH_EXECUTION_SCREENSHOT,fullPage:true});
    if (errors.length) throw new Error(errors.join('\n'));
    console.log('Research plan UI: actual API/database/Temporal checkpoints; search and collection both completed with the page closed; pause/reload/resume and editable import persisted; only upstream public responses substituted.');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exit(1); });
