const {chromium,expect}=require('@playwright/test');
(async()=>{
  const [origin,title]=process.argv.slice(2);
  if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))throw new Error('isolated loopback required');
  const browser=await chromium.launch();
  try {
    const page=await browser.newPage({viewport:{width:1440,height:1000}});
    page.setDefaultTimeout(12000);
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel('用户名',{exact:true}).fill('owner');await page.getByLabel('密码',{exact:true}).fill('test-owner-password-123');await page.getByRole('button',{name:'登录',exact:true}).click();
    const enter=async()=>{
      await page.getByRole('button',{name:`${title} · 继续完善世界与故事`,exact:true}).click();
      await page.getByRole('button',{name:'批量角色与一句话完善',exact:true}).click();
      await expect(page.getByRole('heading',{name:'批量角色与一句话完善',exact:true})).toBeVisible();
    };
    await enter();
    await page.getByRole('button',{name:/两位同名不同命运的悬疑角色.*2人/}).click();
    await expect(page.getByRole('article').filter({hasText:'周守时'})).toHaveCount(2);
    await page.reload();await enter();
    await page.getByLabel('本批人数',{exact:true}).fill('2');
    await page.getByLabel('用一句话或详细文字描述要求',{exact:true}).fill('浏览器批次：城门守卫与他的账房朋友');
    const opened=page.waitForResponse(r=>r.request().method()==='POST'&&/\/character-designs$/.test(r.url()));
    await page.getByRole('button',{name:'建立可编辑角色批次',exact:true}).click();
    const openedResponse=await opened;expect(openedResponse.status()).toBe(201);
    const draft=await openedResponse.json();expect(draft.document.ids).toHaveLength(2);
    const cards=page.getByRole('article');
    await cards.filter({hasText:'待完善角色 1'}).getByRole('button',{name:'查看资料、三视图与关系'}).click();
    await page.getByLabel('角色姓名',{exact:true}).fill('燕闻钟');
    await page.getByLabel('角色背景与简介',{exact:true}).fill('守城门，欠账房朋友一份人情。');
    await expect(page.getByRole('button',{name:'返回小说工作台',exact:true})).toBeDisabled();
    await page.getByRole('button',{name:'保存角色候选',exact:true}).click();
    await expect(page.getByText(/当前候选已保存 · 批次/)).toBeVisible();
    await page.reload();await enter();
    await page.getByRole('button',{name:/浏览器批次：城门守卫与他的账房朋友.*2人/}).click();
    await expect(page.getByRole('article').filter({hasText:'燕闻钟'})).toHaveCount(1);
    const selectGroup=page.getByRole('group',{name:'选择加入本书的人物',exact:true});
    await selectGroup.getByLabel(new RegExp('待完善角色 2')).uncheck();
    await page.getByRole('button',{name:'将选中角色加入本书设定草稿',exact:true}).click();
    await expect(page.getByRole('status').filter({hasText:'选中角色已加入本书设定草稿'})).toBeVisible();
    // Preparing an assistant task from a saved design is free and retains a
    // role-specific method picker. Never start a network-backed task here.
    await page.getByRole('button',{name:'让助手生成或继续完善',exact:true}).click();
    await expect(page.getByRole('heading',{name:'角色设计助手',exact:true})).toBeVisible();
    const prepared=page.waitForResponse(r=>r.request().method()==='POST'&&/\/character-designs\/[^/]+\/assist$/.test(r.url()));
    await page.getByRole('button',{name:/保存请求/}).click();
    const preparedResponse=await prepared;expect(preparedResponse.status()).toBe(201);
    expect((await preparedResponse.json()).frozen.role).toBe('character_designer');
    if(process.env.STUDIO_CHARACTER_SCREENSHOT)await page.screenshot({path:process.env.STUDIO_CHARACTER_SCREENSHOT,fullPage:true});
    expect(errors).toEqual([]);
    process.stdout.write('Character design browser passed: same-name cards, manual edits, reload, partial adoption and free assistant preparation.\n');
  } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
