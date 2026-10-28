import {test,expect} from '@playwright/test';
import fs from 'node:fs';

test.describe.configure({mode:'serial'});

test('手写保存、版本、技能编辑、导出与恢复，无需付费',async({page},testInfo)=>{
 const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('/');
 await expect(page.getByRole('heading',{name:'故事从这里开始。'})).toBeVisible();
 await page.getByRole('button',{name:'开始我的小说'}).click();
 await page.getByLabel('书名',{exact:true}).fill('浏览器验收：雨城');
 await page.getByLabel('创作要求',{exact:true}).fill('青年修锁匠雨夜进城。第一章从城门口开始。');
 await page.getByRole('button',{name:'创建小说',exact:true}).click();
 await expect(page.getByLabel('小说正文')).toBeVisible();
 await page.getByLabel('章节标题').fill('门前');
 await page.getByLabel('小说正文').fill('林舟站在城门前。\n雨水顺着铜钥匙流进掌心，他没有敲门。');
 await page.getByRole('button',{name:'保存草稿',exact:true}).click();
 await expect(page.getByRole('status')).toContainText('已保存新版本');
 const exported=page.waitForEvent('download');
 await page.getByRole('button',{name:'TXT',exact:true}).click();
 const file=await exported;const downloaded=await file.path();
 expect(fs.readFileSync(downloaded!,'utf8')).toContain('雨水顺着铜钥匙');
 expect(fs.readFileSync(downloaded!,'utf8')).toContain('未确认草稿');
 await page.getByRole('button',{name:/专业角色/}).click();
 await page.getByRole('button',{name:'执笔',exact:true}).click();
 await expect(page.getByLabel('工作方法')).toHaveValue(/完整章节/);
 await page.getByLabel('工作方法').fill((await page.getByLabel('工作方法').inputValue())+'\n本书偏好：对话克制，保留行为的歧义。');
 await page.getByRole('button',{name:'保存为本书新版本'}).click();
 await expect(page.getByRole('dialog')).toHaveCount(0);
 const backupPromise=page.waitForEvent('download');await page.getByRole('button',{name:'备份项目',exact:true}).click();
 const backup=await backupPromise;const backupPath=testInfo.outputPath('project.zip');await backup.saveAs(backupPath);
 await page.getByRole('button',{name:/恢复备份/}).click();
 await page.getByLabel('备份文件').setInputFiles(backupPath);
 await page.getByRole('button',{name:'校验并恢复副本'}).click();
 await expect(page.locator('.breadcrumb')).toContainText('恢复副本');
 await expect(page.getByLabel('小说正文')).toHaveValue(/雨水顺着铜钥匙/);
 await page.reload();
 await page.getByRole('button',{name:/浏览器验收：雨城（恢复副本）/}).click();
 await expect(page.getByLabel('小说正文')).toHaveValue(/林舟站在城门前/);
 await page.screenshot({path:testInfo.outputPath('writing-workspace.png'),fullPage:true});
 expect(errors).toEqual([]);
});

test('真实UI调用完整流水线，只有模型传输使用隔离测试夹具',async({page},testInfo)=>{
 await page.goto('/');await page.getByRole('button',{name:'开始我的小说'}).click();
 await page.getByLabel('书名',{exact:true}).fill('流水线测试项目');
 await page.getByLabel('创作要求',{exact:true}).fill('林舟雨夜进城，取钥匙。');
 await page.getByRole('button',{name:'创建小说',exact:true}).click();
 await page.getByRole('button',{name:/生成这一章/}).click();
 await expect(page.getByRole('dialog',{name:'模型与费用'})).toBeVisible();
 await expect(page.getByRole('checkbox',{name:/我同意/})).not.toBeChecked();
 // This host injects only ScriptedProvider: no external network or paid model.
 await page.getByRole('checkbox',{name:/我同意/}).check();
 await page.getByRole('button',{name:'保存费用设置'}).click();
 await page.getByRole('button',{name:/生成这一章/}).click();
 await expect(page.getByLabel('小说正文')).toHaveValue(/城内传来警钟/,{timeout:15000});
 await page.getByRole('tab',{name:'审查',exact:true}).click();
 await expect(page.getByText('核心检查通过 · 待你确认')).toBeVisible();
 await page.getByRole('button',{name:'确认本章为正史'}).click();
 await expect(page.locator('.editor-heading')).toContainText('已确认正史');
 await page.getByRole('tab',{name:'账本',exact:true}).click();
 await expect(page.locator('.ledger-entry')).toContainText('铜钥匙');
 await page.getByRole('button',{name:'＋ 新章',exact:true}).click();
 await expect(page.getByLabel('小说正文')).toHaveValue('');
 await page.waitForTimeout(2500);
 await expect(page.getByLabel('小说正文')).toHaveValue('');
 await expect(page.locator('.editor-heading')).toContainText('第 2 章');
 await page.screenshot({path:testInfo.outputPath('confirmed-workspace.png'),fullPage:true});
});
