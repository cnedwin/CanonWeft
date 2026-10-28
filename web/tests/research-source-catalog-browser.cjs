const {chromium, expect} = require('@playwright/test');
(async () => {
  const [origin,title] = process.argv.slice(2);
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error('isolated loopback required');
  const browser=await chromium.launch();
  try {
    const page=await browser.newPage({viewport:{width:1440,height:1000}});
    page.setDefaultTimeout(15000);
    const errors=[];
    page.on('pageerror',e=>errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel('用户名',{exact:true}).fill('owner');
    await page.getByLabel('密码',{exact:true}).fill('test-owner-password-123');
    await page.getByRole('button',{name:'登录',exact:true}).click();
    async function book() {
      await page.getByRole('button',{name:`${title} · 继续完善世界与故事`,exact:true}).click();
    }
    async function characters() {
      await book();
      await page.getByRole('button',{name:'批量角色与一句话完善',exact:true}).click();
      await page.getByRole('button',{name:/在线素材入库验收.*1人/}).click();
    }
    async function library() {
      await page.getByText('浏览预置姓氏素材',{exact:true}).click();
      await page.getByRole('button',{name:'编辑名字素材库',exact:true}).click();
      return page.getByRole('dialog',{name:'编辑名字素材库',exact:true});
    }
    await characters();
    await page.getByRole('button',{name:'开始风格起名',exact:true}).click();
    await page.getByRole('button',{name:'保存角色候选',exact:true}).click();
    await expect(page.getByText(/当前候选已保存 · 批次/)).toBeVisible();
    let editor=await library();
    await editor.getByText('按题材搜索并采集公开资料',{exact:true}).click();
    await expect(editor.getByRole('combobox',{name:'免费来源',exact:true})).toHaveValue('wikidata_names');
    await editor.getByRole('combobox',{name:'资料语言',exact:true}).selectOption('en');
    await editor.getByLabel('研究关键词',{exact:true}).fill('Liora');
    await editor.getByRole('button',{name:'搜索公开资料',exact:true}).click();
    await editor.getByRole('button',{name:'采集并编辑：Q900000002',exact:true}).click();
    await expect(editor.getByText(/在线名字已加入预览，尚未保存/)).toBeVisible();
    await editor.getByLabel('搜索库中名字素材',{exact:true}).fill('Liora');
    await editor.getByRole('button',{name:'Liora · 个人名 · en',exact:true}).click();
    await expect(editor.getByLabel('素材许可或使用限制',{exact:true})).toHaveValue('CC0-1.0');
    await expect(editor.getByLabel('素材文化与地域',{exact:true})).toHaveValue('unknown');
    await expect(editor.getByLabel('素材来源网址',{exact:true})).toHaveValue(/oldid=71/);
    await editor.getByLabel('素材名字素材',{exact:true}).fill('Liora Vale');
    await editor.getByRole('button',{name:'保存名字素材',exact:true}).click();
    await expect(editor.getByText(/名字素材已保存/)).toBeVisible();
    await editor.getByRole('button',{name:'关闭素材库',exact:true}).click();
    await page.reload(); await characters();
    editor=await library();
    await editor.getByLabel('搜索库中名字素材',{exact:true}).fill('Liora Vale');
    await editor.getByRole('button',{name:'Liora Vale · 个人名 · en',exact:true}).click();
    await expect(editor.getByLabel('素材来源网址',{exact:true})).toHaveValue(/oldid=71/);
    await editor.getByRole('button',{name:'关闭素材库',exact:true}).click();
    await page.goto(origin); await book();
    await page.getByRole('button',{name:'题材研究与事实卡',exact:true}).click();
    await page.getByRole('button',{name:/^开放论文研究 · 第\d+版/}).click();
    await page.getByRole('button',{name:'采集、导入与采用资料',exact:true}).click();
    await page.getByText('按题材搜索并采集公开资料',{exact:true}).click();
    await page.getByRole('combobox',{name:'免费来源',exact:true}).selectOption('europe_pmc');
    await expect(page.getByRole('combobox',{name:'资料语言',exact:true})).toHaveValue('en');
    await page.getByLabel('研究关键词',{exact:true}).fill('wound care');
    await page.getByRole('button',{name:'搜索公开资料',exact:true}).click();
    await page.getByRole('button',{name:'采集并编辑：Wound care',exact:true}).click();
    await expect(page.getByLabel('导入内容 · 可修改',{exact:true})).toHaveValue(/CC-BY-4.0/);
    await expect(page.getByLabel('导入内容 · 可修改',{exact:true})).toHaveValue(/10.1234\/example/);
    if (process.env.STUDIO_SOURCE_CATALOG_SCREENSHOT) await page.screenshot({path:process.env.STUDIO_SOURCE_CATALOG_SCREENSHOT,fullPage:true});
    if (errors.length) throw new Error(errors.join('\n'));
    console.log('Real Vue/HTTP: Wikidata collection -> private name preview -> manual edit/save -> reload with original revision; Europe PMC selection -> licensed original import. Upstream responses substituted, no model calls.');
  } finally { await browser.close(); }
})().catch(e=>{console.error(e);process.exit(1)});
