// Actual Vue/HTTP/isolated MySQL. Go's local transport produced the candidate;
// browser does not start provider calls or Temporal executions.
const { chromium, expect } = require('@playwright/test');
(async () => {
  const [origin, task, title] = process.argv.slice(2);
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error('isolated loopback required');
  const browser = await chromium.launch(); let page;
  try {
    page = await browser.newPage(); page.setDefaultTimeout(10000);
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel('用户名', {exact:true}).fill('owner');
    await page.getByLabel('密码', {exact:true}).fill('test-owner-password-123');
    await page.getByRole('button', {name:'登录',exact:true}).click();
    const enter = async () => {
      let releaseInitial;
      if(process.env.STUDIO_EXPORT_BROWSER==='1') {
        const initialGate=new Promise(resolve=>{releaseInitial=resolve});
        await page.route('**/api/novels/*/plans',async route=>{await initialGate;await route.continue();},{times:1});
      }
      await page.getByRole('button', {name:`${title} · 继续完善世界与故事`,exact:true}).click();
      await page.getByRole('button', {name:'规划世界、大纲与细纲',exact:true}).click();
      if(releaseInitial) {
        try {await expect(page.getByRole('button',{name:'全书与分卷安排',exact:true})).toBeDisabled();}
        finally {releaseInitial();}
      }
      await page.getByRole('button', {name:'全书与分卷安排',exact:true}).click();
      await page.getByRole('button', {name:'编排本卷章节',exact:true}).click();
      await page.getByLabel('第1章安排',{exact:true}).getByRole('button', {name:'完善本章细纲',exact:true}).click();
      await page.getByRole('button', {name:'开始本章写作',exact:true}).click();
      await expect(page.getByRole('heading', {name:'章节写作',exact:true})).toBeVisible();
    };
    await enter();
    await expect(page.getByLabel('章节正文', {exact:true})).toHaveValue('林舟在门外停了下来。');
    await page.getByRole('button', {name:'请 Agent 写作或修改',exact:true}).click();
    await expect(page.getByLabel('章节正文', {exact:true})).toBeEnabled();
    await page.getByRole('button', {name:`查看任务 ${task.slice(0,6)}`,exact:true}).click();
    await page.getByRole('button', {name:'查看正文候选',exact:true}).click();
    await expect(page.getByRole('region', {name:'正文候选对照'})).toContainText('只完成开场，余下场景仍需续写。');
    await expect(page.getByLabel('章节正文', {exact:true})).toHaveValue('林舟在门外停了下来。');
    await page.getByRole('button', {name:'将候选加入私人稿',exact:true}).click();
    await expect(page.getByLabel('章节正文', {exact:true})).toBeEnabled();
    await expect(page.getByLabel('章节正文', {exact:true})).toHaveValue('林舟在门外停了下来。门缝里露出一角湿透的信纸。');
    const revised = '林舟在门外停了下来。门缝里露出一角湿透的信纸。他听见门后有人屏住了呼吸。';
    await page.getByLabel('章节正文', {exact:true}).fill(revised);
    await page.getByRole('button', {name:'保存私人稿',exact:true}).click();
    await expect(page.getByRole('button', {name:'保存并返回规划',exact:true})).toBeEnabled();
    await page.reload(); await enter();
    await expect(page.getByLabel('章节正文', {exact:true})).toHaveValue(revised);
    await page.getByRole('button', {name:'请 Agent 写作或修改',exact:true}).click();
    await page.getByLabel('希望 AI 协助的内容', {exact:true}).fill('保留已有段落，续写发现纸条后的选择与代价');
    const picker = page.getByLabel('固定角色版本', {exact:true});
    await expect(picker.locator('option').nth(1)).toContainText('章节写作');
    await picker.selectOption({index:1});
    const agentID = await picker.inputValue();
    await expect(page.getByText('当前草稿、作者要求和资料由工作台填入；无需填写技术格式。可在方法与能力页面编辑方法后选择新版本试用。', {exact:true})).toBeVisible();
    const prepared = page.waitForResponse(r => r.request().method() === 'POST' && /\/chapters\/[^/]+\/writer$/.test(r.url()));
    await page.getByRole('button', {name:'保存请求并估算费用',exact:true}).click();
    const response = await prepared;
    expect(response.ok()).toBe(true);
    const saved = await response.json();
    expect(saved.frozen.capability.agent_version_id).toBe(agentID);
    expect(saved.frozen.capability.mode).toBe('TRIAL');
    await expect(page.getByText('本章原稿、细纲和写作要求已保存，尚未调用模型。', {exact:true})).toBeVisible();
    if (process.env.STUDIO_POLISH_BROWSER === '1') {
      await page.reload(); await enter();
      const desk = page.getByRole('region', {name:'自然文风与投稿前自检'});
      await expect(desk).toBeVisible();
      await desk.getByLabel('目标平台', {exact:true}).selectOption('fanqie');
      await desk.getByLabel('目标读者', {exact:true}).selectOption('女频');
      await desk.getByRole('button', {name:'让助手自然文风精修',exact:true}).click();
      await expect(page.getByLabel('希望 AI 协助的内容', {exact:true})).toHaveValue(/目标：番茄小说/);
      const polishing = page.waitForResponse(r => r.request().method()==='POST' && /\/chapters\/[^/]+\/writer$/.test(r.url()));
      await page.getByRole('button',{name:'保存请求并估算费用',exact:true}).click();
      const polishResponse=await polishing;
      expect(polishResponse.ok()).toBe(true);
      expect(polishResponse.request().postDataJSON().method).toBe('fanqie-polish');
      await page.reload();
      await page.getByRole('button',{name:`${title} · 继续完善世界与故事`,exact:true}).click();
      await page.getByRole('button',{name:'快速连写 · 最多10章',exact:true}).click();
      await page.getByLabel('本批总费用上限', {exact:false}).fill('0.1');
      const batchResponse=page.waitForResponse(r=>r.request().method()==='POST' && /\/writing-batches$/.test(r.url()));
      await page.getByRole('button',{name:'准备 1 章',exact:true}).click();
      expect((await batchResponse).ok()).toBe(true);
      await expect(page.getByRole('region',{name:'连续章节进度'})).toContainText('已准备，尚未调用');
      if(process.env.STUDIO_NOVEL_SCREENSHOT) await page.screenshot({path:process.env.STUDIO_NOVEL_SCREENSHOT,fullPage:true});
      await page.reload();
      await page.getByRole('button',{name:`${title} · 继续完善世界与故事`,exact:true}).click();
      await page.getByRole('button',{name:'快速连写 · 最多10章',exact:true}).click();
      await expect(page.getByRole('button',{name:/1章 · 已准备，尚未调用/})).toBeVisible();
      console.log('Actual browser verified specialized polish request, persisted batch preparation and reload recovery; no paid calls.');
    }
    if (process.env.STUDIO_EXPORT_BROWSER === '1') {
      await page.reload();
      await page.getByRole('button',{name:`${title} · 继续完善世界与故事`,exact:true}).click();
      await page.getByRole('button',{name:'整书与分卷导出',exact:true}).click();
      await expect(page.getByRole('region',{name:'导出内容预览',exact:true})).toContainText('可导出 1 章');
      await expect(page.getByRole('button',{name:'下载 EPUB 电子书',exact:true})).toBeDisabled();
      await page.getByLabel('导出作者署名',{exact:true}).fill('浏览器验证作者');
      await page.getByRole('button',{name:'检查导出内容',exact:true}).click();
      await expect(page.getByRole('button',{name:'下载 EPUB 电子书',exact:true})).toBeDisabled();
      await page.getByRole('checkbox',{name:'我已核对缺章与提示，导出当前可用稿，并在书籍信息中保留说明',exact:true}).check();
      await expect(page.getByRole('button',{name:'下载 EPUB 电子书',exact:true})).toBeEnabled();
      const epubPromise=page.waitForEvent('download');
      await page.getByRole('button',{name:'下载 EPUB 电子书',exact:true}).click();
      const epub=await epubPromise;
      expect(epub.suggestedFilename()).toMatch(/\.epub$/);
      const fs=require('node:fs');
      const epubBytes=fs.readFileSync(await epub.path());
      expect(epubBytes.subarray(0,2).toString()).toBe('PK');
      expect(epubBytes.includes(Buffer.from('EPUB/package.opf'))).toBe(true);
      if(process.env.STUDIO_EXPORT_EPUB)await epub.saveAs(process.env.STUDIO_EXPORT_EPUB);
      const txtPromise=page.waitForEvent('download');
      await page.getByRole('button',{name:'下载整合 TXT',exact:true}).click();
      const txt=await txtPromise;
      expect(fs.readFileSync(await txt.path(),'utf8')).toContain(revised);
      const pdfPromise=page.waitForEvent('download');
      await page.getByRole('button',{name:'下载 PDF 排版稿',exact:true}).click();
      const pdf=await pdfPromise;
      expect(pdf.suggestedFilename()).toMatch(/\.pdf$/);
      expect(fs.readFileSync(await pdf.path()).subarray(0,5).toString()).toBe('%PDF-');
      if(process.env.STUDIO_EXPORT_PDF)await pdf.saveAs(process.env.STUDIO_EXPORT_PDF);
      if(process.env.STUDIO_EXPORT_SCREENSHOT)await page.screenshot({path:process.env.STUDIO_EXPORT_SCREENSHOT,fullPage:true});
      await page.getByLabel('导出正文来源',{exact:true}).selectOption('published');
      await page.getByRole('button',{name:'检查导出内容',exact:true}).click();
      await expect(page.getByRole('region',{name:'导出内容预览',exact:true})).toContainText('可导出 0 章');
      await expect(page.getByRole('button',{name:'下载 EPUB 电子书',exact:true})).toBeDisabled();
      console.log('Actual Vue export downloaded PDF, EPUB and TXT with saved private prose; author requirement and unpublished exclusion verified.');
    }
    expect(errors).toEqual([]);
    console.log('Actual chapter browser: confirmed detail -> preserved private prose -> stored task -> candidate notes/comparison -> explicit adoption -> manual save -> reload -> new request. No real model calls or publication.');
  } catch (e) { console.error(await page?.locator('body').innerText()); throw e; }
  finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
