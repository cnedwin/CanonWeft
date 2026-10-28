const {chromium,expect}=require('@playwright/test');
(async()=>{
  const [origin,title]=process.argv.slice(2);
  if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error('isolated loopback required');
  const browser=await chromium.launch();let page;
  try{
    page=await browser.newPage();page.setDefaultTimeout(10000);
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel('用户名',{exact:true}).fill('owner');
    await page.getByLabel('密码',{exact:true}).fill('test-owner-password-123');
    await page.getByRole('button',{name:'登录',exact:true}).click();
    const enter=async()=>{
      await page.getByRole('button',{name:`${title} · 继续完善世界与故事`,exact:true}).click();
      for(const name of ['规划世界、大纲与细纲','全书与分卷安排','编排本卷章节']) await page.getByRole('button',{name,exact:true}).click();
      await page.getByRole('article',{name:'第1章安排',exact:true}).getByRole('button',{name:'完善本章细纲',exact:true}).click();
      await page.getByRole('button',{name:'开始本章写作',exact:true}).click();
    };
    await enter();
    const before=await page.getByLabel('章节正文',{exact:true}).inputValue();
    await page.getByRole('button',{name:'提取与编辑本章账本',exact:true}).click();
    await page.getByRole('button',{name:'继续查看本章审稿',exact:true}).click();
    const publish=page.getByRole('button',{name:'确认采用并发布本章',exact:true});
    await expect(publish).toBeEnabled();
    await publish.click();
    await expect(page.getByText('本章正文、剧情账本与对应记忆已发布，可以继续编辑或返回规划。',{exact:true})).toBeVisible();
    await expect(page.getByLabel('章节正文',{exact:true})).toBeEnabled();
    await expect(page.getByLabel('章节正文',{exact:true})).toHaveValue(before);
    await page.reload();await enter();
    await expect(page.getByLabel('章节正文',{exact:true})).toHaveValue(before);
    expect(errors).toEqual([]);
    console.log('Actual chapter publication browser: selected independent review -> explicit publish -> unlocked rebased draft -> reload preserved prose. No browser model execution.');
  }catch(e){console.error(await page?.locator('body').innerText());throw e;}finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
