// Uses the actual built UI, HTTP services, isolated MySQL and local embeddings.
const { chromium, expect } = require("@playwright/test");
(async()=>{
  const origin=process.argv[2]; if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error("isolated loopback server required");
  const browser=await chromium.launch(); let page;
  try {
    page=await browser.newPage(); page.setDefaultTimeout(10000);
    const errors=[]; page.on("pageerror",e=>errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel("用户名",{exact:true}).fill("owner"); await page.getByLabel("密码",{exact:true}).fill("test-owner-password-123"); await page.getByRole("button",{name:"登录",exact:true}).click();
    const enter=async()=>{ await page.getByRole("button",{name:"规划验收小说 · 继续完善世界与故事",exact:true}).click(); await page.getByRole("button",{name:"规划世界、大纲与细纲",exact:true}).click(); };
    const fill=async(fields)=>{for(const [label,value] of Object.entries(fields)) await page.getByLabel(label,{exact:true}).fill(value);};
    const adopt=async()=>{
      await page.getByRole("button",{name:"更新并核对规划依据",exact:true}).click();
      await expect(page.getByRole("button",{name:"查看规划候选",exact:true})).toBeEnabled();
      await page.getByRole("button",{name:"查看规划候选",exact:true}).click();
      await expect(page.getByRole("region",{name:"规划候选对照"})).toBeVisible();
      await page.getByLabel("我已查看本版内容与依据，确认采用此规划").check();
      await page.getByRole("button",{name:"确认采用此规划",exact:true}).click();
      await expect(page.getByText("本版规划已确认采用，后续步骤会引用此版本。",{exact:true})).toBeVisible();
    };
    await enter();
    await fill({"规划标题":"雾城世界与故事","内容摘要":"医者寻找失踪师父","主线目标与因果":"调查旧案，找到师父","核心主题":"责任与真相","世界规则":"医术不能复活死人","能力与行动的代价":"治疗消耗稀缺药材","世界边界与不可违背事项":"不能穿越时间","时空、地点与社会背景":"雾城第一年","势力及其冲突":"药师会与巡夜司争夺资源","主要人物与关系":"林舟与师父互相信任","人物目标、阻碍与成长弧线":"林舟从逃避到承担责任","重大转折与高光时刻":"发现师父主动失踪后救出师父","结局方向":"公开证据并修复信任"});
    await adopt();
    await page.getByRole("button",{name:"全书与分卷安排",exact:true}).click();
    await fill({"规划标题":"雾城全书大纲","内容摘要":"调查、营救与公开证据","主线目标与因果":"寻找师父","核心主题":"责任与真相","结局方向":"公开证据"});
    await page.getByRole("button",{name:"添加一卷",exact:true}).click();
    await page.getByLabel("分卷摘要",{exact:true}).fill("追查旧案并营救师父");
    await page.getByRole("button",{name:"编排本卷章节",exact:true}).click();
    await expect(page.getByLabel("规划标题",{exact:true})).toHaveValue("第1卷");
    await fill({"内容摘要":"雨夜追踪","主线目标与因果":"找到师父留下的线索"});
    await page.getByRole("button",{name:"添加一章",exact:true}).click();
    await fill({"章节标题":"雨夜","章节摘要":"找到秘密纸条","叙事视角":"林舟限知","本章目标":"找到足迹","关键事件":"进入药铺","人物状态变化":"决定面对真相","前后衔接":"纸条引向旧城"});
    await adopt();
    await page.getByRole("button",{name:"返回全书安排",exact:true}).click();
    await expect(page.getByLabel("分卷摘要",{exact:true})).toHaveValue("追查旧案并营救师父");
    await adopt();
    await page.getByRole("button",{name:"编排本卷章节",exact:true}).click();
    await page.getByRole("button",{name:"完善本章细纲",exact:true}).click();
    await page.getByLabel("内容摘要",{exact:true}).fill("进入药铺并找到纸条");
    await page.getByRole("button",{name:"添加场景",exact:true}).click();
    await fill({"故事时间":"第一夜","场景地点":"药铺","在场人物":"林舟","视角可知信息":"只知道师父失踪","场景目标":"寻找足迹","场景冲突":"正门被封","人物行动":"绕到后门进入","行动结果":"找到纸条","情绪变化":"疑惑转为不安","伏笔与回收":"纸条上的暗号","因果前置":"已听闻师父失踪","场景结尾与钩子":"纸条署名是自己"});
    await adopt();
    await page.reload(); await enter();
    await page.getByRole("button",{name:"全书与分卷安排",exact:true}).click();
    await page.getByRole("button",{name:"编排本卷章节",exact:true}).click();
    await page.getByRole("button",{name:"完善本章细纲",exact:true}).click();
    await expect(page.getByLabel("故事时间",{exact:true})).toHaveValue("第一夜");
    await expect(page.getByLabel("场景结尾与钩子",{exact:true})).toHaveValue("纸条署名是自己");
    expect(errors).toEqual([]);
    console.log("Actual novel planning: world -> volume chapters -> whole-book outline -> chapter detail, explicit adoption and reload persistence passed; no model call.");
  } catch(e) { console.error(await page?.locator("body").innerText()); throw e; } finally { await browser.close(); }
})().catch(e=>{console.error(e);process.exitCode=1;});
