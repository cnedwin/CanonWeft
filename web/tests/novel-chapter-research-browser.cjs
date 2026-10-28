const { chromium, expect } = require('@playwright/test');
const path = require('node:path');
(async () => {
  const [origin, title, phase] = process.argv.slice(2);
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error('isolated loopback required');
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    page.setDefaultTimeout(12000);
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel('用户名', { exact: true }).fill('owner');
    await page.getByLabel('密码', { exact: true }).fill('test-owner-password-123');
    await page.getByRole('button', { name: '登录', exact: true }).click();
    async function enter() {
      await page.getByRole('button', { name: `${title} · 继续完善世界与故事`, exact: true }).click();
      await page.getByRole('button', { name: '规划世界、大纲与细纲', exact: true }).click();
      await page.getByRole('button', { name: '全书与分卷安排', exact: true }).click();
      await page.getByRole('button', { name: '编排本卷章节', exact: true }).click();
      await page.getByLabel('第1章安排', { exact: true }).getByRole('button', { name: '完善本章细纲', exact: true }).click();
      await page.getByRole('button', { name: '开始本章写作', exact: true }).click();
      await expect(page.getByRole('heading', { name: '章节写作', exact: true })).toBeVisible();
    }
    await enter();
    const desk = page.getByRole('region', { name: '本章专业参考', exact: true });
    const originalText = await page.getByLabel('章节正文', { exact: true }).inputValue();
    if (phase === 'refresh') {
      await expect(page.getByRole('alert').filter({ hasText: '本章引用资料已换版' })).toBeVisible();
      await desk.getByRole('button', { name: '核对引用原文', exact: true }).click();
      await expect(desk.getByRole('alert')).toContainText('来源已换版');
      expect(originalText).toContain('作者继续保留并修改原稿。');
    }
    await desk.getByLabel('查找本章资料', { exact: true }).fill('湿纸 修复');
    await desk.getByRole('button', { name: '检索本章资料', exact: true }).click();
    const hit = desk.locator('article').filter({ hasText: '纸张修复专业参考' });
    await expect(hit).toHaveCount(1);
    await hit.getByRole('button', { name: '查看资料原文', exact: true }).click();
    await expect(desk.locator('pre')).toContainText('湿纸不能用力揭开');
    if (phase === 'select') {
      await hit.getByRole('button', { name: '加入本章参考', exact: true }).click();
      await desk.getByLabel('原文短引', { exact: true }).fill('湿纸不能用力揭开');
      await desk.getByLabel('本章用途', { exact: true }).fill('林舟发现湿信后谨慎查看');
      await desk.getByLabel('适用年代', { exact: true }).fill('需核对故事年代的纸张材质');
      await desk.getByLabel('适用地域', { exact: true }).fill('旧城');
      await desk.getByLabel('适用限制与待核对处', { exact: true }).fill('仅支持避免粗暴揭纸，不确定具体修复工艺');
    } else {
      await hit.getByRole('button', { name: '改用此版本并重新核对', exact: true }).click();
      await desk.getByLabel('原文短引', { exact: true }).fill('湿纸不能用力揭开');
      await desk.getByLabel('适用限制与待核对处', { exact: true }).fill('已核对新版：具体措施必须由专业人员判断');
    }
    await page.getByRole('button', { name: '保存私人稿', exact: true }).click();
    await expect(desk.getByRole('button', { name: '检索本章资料', exact: true })).toBeEnabled();
    await page.reload();
    await enter();
    await expect(desk.getByLabel('原文短引', { exact: true })).toHaveValue('湿纸不能用力揭开');
    await expect(desk.getByLabel('本章用途', { exact: true })).toHaveValue('林舟发现湿信后谨慎查看');
    await expect(page.getByLabel('章节正文', { exact: true })).toHaveValue(originalText);
    await expect(page.getByRole('alert').filter({ hasText: '本章引用资料已换版' })).toHaveCount(0);
    if (phase === 'refresh') {
      await expect(desk.getByLabel('适用限制与待核对处', { exact: true })).toHaveValue('已核对新版：具体措施必须由专业人员判断');
    }
    await desk.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.resolve(__dirname, '../../../work/novel-chapter-research-' + phase + '.png') });
    expect(errors).toEqual([]);
    console.log('Chapter research ' + phase + ': actual search, original reading, references saved/reloaded, prose preserved; no model calls');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
