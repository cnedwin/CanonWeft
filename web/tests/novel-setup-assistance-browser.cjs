// Real Vue/HTTP/MySQL fixture; model output was prepared by the local transport.
const { chromium, expect } = require('@playwright/test');
(async () => {
  const [origin, task] = process.argv.slice(2);
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error('isolated origin required');
  const browser = await chromium.launch();
  let page;
  try {
    page = await browser.newPage();
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('dialog', d => d.accept());
    await page.goto(origin);
    await page.getByLabel('用户名', {exact:true}).fill('owner');
    await page.getByLabel('密码', {exact:true}).fill('test-owner-password-123');
    await page.getByRole('button', {name:'登录',exact:true}).click();
    const enter = async (title) => {
      await page.getByRole('button', {name:'新建小说',exact:true}).click();
      await page.getByRole('button', {name:`${title} · 第2步`,exact:true}).click();
      await page.getByRole('button', {name:'用想法或文件让助手完善',exact:true}).click();
      await expect(page.getByLabel('修钟题材资料',{exact:true})).toBeVisible();
    };
    await enter('建书助手测试');
    await page.getByLabel('修钟题材资料',{exact:true}).check();
    await page.getByLabel('我的想法与完善要求').fill('核对资料，查看已有建议');
    await page.getByRole('button', {name:'打开建书助手',exact:true}).click();
    await page.getByRole('button',{name:`查看任务 ${task.slice(0,6)}`,exact:true}).click();
    await page.getByRole('button',{name:'查看完善建议',exact:true}).click();
    const suggestion = page.getByRole('region',{name:'建书字段建议'});
    await expect(suggestion).toContainText('资料依据');
    await suggestion.getByLabel('采用世界设定',{exact:true}).uncheck();
    await suggestion.getByLabel('书名建议',{exact:true}).fill('停钟旧事（作者修订）');
    if (process.env.STUDIO_SETUP_ASSIST_SCREENSHOT) await suggestion.screenshot({path:process.env.STUDIO_SETUP_ASSIST_SCREENSHOT});
    await suggestion.getByRole('button',{name:'采用所选 1 项到建书草稿',exact:true}).click();
    await expect(page.getByRole('status')).toContainText('所选建议已保存');
    await page.reload();
    await enter('停钟旧事（作者修订）');
    await expect(page.getByLabel('修钟题材资料',{exact:true})).toBeVisible();
    expect(errors).toEqual([]);
    console.log('Actual Vue + HTTP + MySQL: precreation materials, existing task, editable per-field adoption, persisted title and same materials after reload; no model call.');
  } catch (e) {
    console.error(await page?.locator('body').innerText());
    throw e;
  } finally { await browser.close(); }
})().catch(e=>{console.error(e);process.exitCode=1});
