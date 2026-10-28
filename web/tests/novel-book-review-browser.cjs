const { chromium, expect } = require('@playwright/test');
(async()=>{
  const [origin,title,runID]=process.argv.slice(2);
  if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))throw new Error('isolated loopback required');
  const browser=await chromium.launch();
  try {
    const page=await browser.newPage({viewport:{width:1440,height:1000}});
    page.setDefaultTimeout(15000);
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel('用户名',{exact:true}).fill('owner');
    await page.getByLabel('密码',{exact:true}).fill('test-owner-password-123');
    await page.getByRole('button',{name:'登录',exact:true}).click();
    const enter=async()=>{
      await page.getByRole('button',{name:`${title} · 继续完善世界与故事`,exact:true}).click();
      await page.getByRole('button',{name:'整卷与全书审稿',exact:true}).click();
      await expect(page.getByRole('button',{name:new RegExp(`已完成.*${runID.slice(0,8)}`)})).toBeVisible();
    };
    await enter();
    await page.getByRole('button',{name:new RegExp(`已完成.*${runID.slice(0,8)}`)}).click();
    const report=page.getByRole('article',{name:'审稿报告'});
    await expect(report.getByText(/报告已过期/)).toBeVisible();
    await expect(report.getByText(/已核对报告覆盖 10 \/ 10 章/)).toBeVisible();
    await expect(report.getByRole('heading',{name:'优先修订 · 铜钥匙缺少归还过程'})).toBeVisible();
    await expect(report.getByText('林舟把铜钥匙交给阿岚。',{exact:true})).toBeVisible();
    await page.reload();await enter();
    await page.getByRole('button',{name:new RegExp(`已完成.*${runID.slice(0,8)}`)}).click();
    await expect(report.getByRole('heading',{name:'优先修订 · 铜钥匙缺少归还过程'})).toBeVisible();
    if(process.env.STUDIO_REVIEW_SCREENSHOT)await page.screenshot({path:process.env.STUDIO_REVIEW_SCREENSHOT,fullPage:true});
    await page.getByLabel('审稿重点',{exact:true}).fill('重新检查铜钥匙的归还是否充分');
    await page.getByLabel('审稿费用上限',{exact:true}).fill('1');
    await page.getByRole('button',{name:'检查审稿范围',exact:true}).click();
    await expect(page.getByText(/将读取 10 章，分 2 段/)).toBeVisible();
    const saved=page.waitForResponse(r=>r.request().method()==='POST'&&/\/book-reviews$/.test(r.url()));
    await page.getByRole('button',{name:'保存审稿任务（不调用模型）',exact:true}).click();
    const response=await saved;expect(response.status()).toBe(201);
    const task=await response.json();expect(task.state).toBe('PREPARED');
    await expect(page.getByRole('button',{name:'开始审稿（将调用模型）',exact:true})).toBeVisible();
    await page.getByRole('button',{name:new RegExp(`已完成.*${runID.slice(0,8)}`)}).click();
    await page.getByRole('button',{name:'打开《雨夜来信》编辑',exact:true}).click();
    await expect(page.getByLabel('章节正文',{exact:true})).toHaveValue(/林舟把铜钥匙交给阿岚。/);
    expect(errors).toEqual([]);
    console.log('review browser passed: 10 chapter coverage, evidence, stale report, reload, prepare without model calls, chapter navigation');
  } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
