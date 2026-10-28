// Real UI/API/MySQL. The completed task was executed by the Go test's local
// model transport; this browser never starts a real provider or Temporal job.
const {chromium, expect}=require('@playwright/test');
(async()=>{
  const [origin,task,agent]=process.argv.slice(2);
  if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error('isolated server required');
  const browser=await chromium.launch();let page;
  try {
    page=await browser.newPage();page.setDefaultTimeout(10000);
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel('用户名',{exact:true}).fill('owner');
    await page.getByLabel('密码',{exact:true}).fill('test-owner-password-123');
    await page.getByRole('button',{name:'登录',exact:true}).click();
    const enter=async()=>{
      await page.getByRole('button',{name:'守灯人 · 继续完善世界与故事',exact:true}).click();
      await page.getByRole('button',{name:'规划世界、大纲与细纲',exact:true}).click();
    };
    await enter();
    await page.getByRole('button',{name:'请 Agent 帮我完善当前规划',exact:true}).click();
    await expect(page.getByRole('heading',{name:'规划助手',exact:true})).toBeVisible();
    await expect(page.getByLabel('主线目标与因果',{exact:true})).toBeDisabled();
    await page.getByRole('button',{name:`查看任务 ${task.slice(0,6)}`,exact:true}).click();
    await page.getByRole('button',{name:'查看规划修改建议',exact:true}).click();
    await expect(page.getByRole('region',{name:'助手建议对照'})).toContainText('聚焦寻找妹妹的线索');
    await page.getByRole('button',{name:'将建议加入私人草稿',exact:true}).click();
    await expect(page.getByLabel('主线目标与因果',{exact:true})).toHaveValue('循药方中的错误剂量追查妹妹失踪的原因');
    await expect(page.getByLabel('主线目标与因果',{exact:true})).toBeEnabled();
    await page.getByRole('button',{name:'请 Agent 帮我完善当前规划',exact:true}).click();
    await page.getByLabel('希望 AI 协助的内容',{exact:true}).fill('保留当前主线，只增加医术所需的代价与限制');
    await page.getByLabel('固定角色版本',{exact:true}).selectOption(agent);
    await expect(page.getByText('正在检查完整依赖与撤销状态…',{exact:true})).toHaveCount(0);
    await expect(page.getByLabel('完整试评输入',{exact:true})).toHaveCount(0);
    await page.getByRole('button',{name:'保存请求并估算费用',exact:true}).click();
    await expect(page.getByText('规划要求与引用资料已保存，尚未调用模型。',{exact:true})).toBeVisible();
    await page.getByRole('button',{name:'返回手工规划',exact:true}).click();
    await page.reload();await enter();
    await expect(page.getByLabel('主线目标与因果',{exact:true})).toHaveValue('循药方中的错误剂量追查妹妹失踪的原因');
    expect(errors).toEqual([]);
    console.log('Actual planning assistance browser: restored task -> structured proposal comparison -> explicit private draft save -> new feedback preparation -> reload; no auto-publication or provider execution.');
  } catch(e){console.error(await page?.locator('body').innerText());throw e;}finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
