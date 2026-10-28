const {chromium, expect}=require('@playwright/test');
(async()=>{
  const [origin,title]=process.argv.slice(2);
  if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))throw Error('isolated loopback required');
  const browser=await chromium.launch();
  try{
    const page=await browser.newPage({viewport:{width:1440,height:1000}});
    page.setDefaultTimeout(15000);
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel('用户名',{exact:true}).fill('owner');
    await page.getByLabel('密码',{exact:true}).fill('test-owner-password-123');
    await page.getByRole('button',{name:'登录',exact:true}).click();
    async function book(){await page.getByRole('button',{name:`${title} · 继续完善世界与故事`,exact:true}).click();}
    async function characters(){
      await book();
      await page.getByRole('button',{name:'批量角色与一句话完善',exact:true}).click();
      await page.getByRole('button',{name:/批量原件验收.*1人/}).click();
    }
    async function openLibrary(){
      await page.getByText('浏览预置姓氏素材',{exact:true}).click();
      await page.getByRole('button',{name:'编辑名字素材库',exact:true}).click();
      return page.getByRole('dialog',{name:'编辑名字素材库',exact:true});
    }
    await characters();
    await page.getByRole('button',{name:'开始风格起名',exact:true}).click();
    await page.getByRole('button',{name:'保存角色候选',exact:true}).click();
    await expect(page.getByText(/当前候选已保存 · 批次/)).toBeVisible();
    let library=await openLibrary();
    await library.getByText('批量整理已采集的名字',{exact:true}).click();
    await library.getByRole('button',{name:'读取已采集名字',exact:true}).click();
    await expect(library.getByText(/本页\s*3\s*份/)).toBeVisible();
    await library.getByRole('button',{name:'选择本页名字原件',exact:true}).click();
    await library.getByRole('button',{name:'预览所选名字入库',exact:true}).click();
    await expect(library.getByText(/新增2条/)).toBeVisible();
    await expect(library.getByRole('button',{name:'关闭素材库',exact:true})).toBeDisabled();
    await library.getByLabel('搜索库中名字素材',{exact:true}).fill('Elowen kept');
    await expect(library.getByRole('button',{name:'Elowen kept · 个人名 · en',exact:true})).toBeVisible();
    await library.getByLabel('搜索库中名字素材',{exact:true}).fill('Neris');
    await library.getByRole('button',{name:'Neris · 个人名 · en',exact:true}).click();
    await library.getByLabel('素材名字素材',{exact:true}).fill('Neris edited');
    await library.getByRole('button',{name:'保存名字素材',exact:true}).click();
    await expect(library.getByText(/名字素材已保存/)).toBeVisible();
    await library.getByRole('button',{name:'关闭素材库',exact:true}).click();
    await page.reload();await characters();library=await openLibrary();
    await library.getByLabel('搜索库中名字素材',{exact:true}).fill('Neris edited');
    await library.getByRole('button',{name:'Neris edited · 个人名 · en',exact:true}).click();
    await expect(library.getByLabel('素材来源网址',{exact:true})).toHaveValue(/oldid=71/);
    if(process.env.STUDIO_OFFICIAL_BATCH_SCREENSHOT)await page.screenshot({path:process.env.STUDIO_OFFICIAL_BATCH_SCREENSHOT.replace(/\.png$/,'-names.png'),fullPage:true});
    await library.getByRole('button',{name:'关闭素材库',exact:true}).click();
    await page.goto(origin);await book();
    await page.getByRole('button',{name:'题材研究与事实卡',exact:true}).click();
    await page.getByRole('button',{name:/^制度链接研究 · 第\d+版/}).click();
    await page.getByRole('button',{name:'采集、导入与采用资料',exact:true}).click();
    await page.getByText('按题材搜索并采集公开资料',{exact:true}).click();
    await page.getByRole('combobox',{name:'免费来源',exact:true}).selectOption('official_reference');
    await page.getByLabel('研究关键词',{exact:true}).fill('https://www.gov.cn/zhengce/test-article.htm');
    await page.getByRole('button',{name:'搜索公开资料',exact:true}).click();
    await page.getByRole('button',{name:'采集并编辑：官方原文 · www.gov.cn',exact:true}).click();
    await expect(page.getByLabel('导入内容 · 可修改',{exact:true})).toHaveValue(/未保存HTML全文|并非原始HTML全文/);
    await expect(page.getByText(/待核对 1 项/)).toBeVisible();
    const link=page.getByRole('link',{name:'下载原文件核对',exact:true});
    const response=await page.context().request.get(new URL(await link.getAttribute('href'),origin).href,{headers:{Origin:origin}});
    if(!response.ok())throw Error('receipt download '+response.status());
    const receipt=await response.json();
    if(receipt.schema!=='official-reference-receipt-v1'||[...receipt.excerpt].length>160||JSON.stringify(receipt).includes('FULLTEXT_CANARY'))throw Error('capture receipt/full-text boundary failed');
    if(process.env.STUDIO_OFFICIAL_BATCH_SCREENSHOT)await page.screenshot({path:process.env.STUDIO_OFFICIAL_BATCH_SCREENSHOT,fullPage:true});
    if(errors.length)throw Error(errors.join('\n'));
    console.log('Real Vue/HTTP: select multiple saved name originals -> preview preserving author edits -> save/reload; official link -> explicit limited capture -> download JSON receipt, pending-partial gate remains. Only upstream responses substituted; no model calls.');
  }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
