export const editorialPlatforms = [
  {
    id: "general",
    name: "通用创作",
    hint: "按题材、目标读者和作者文风判断，不套固定节奏。",
  },
  {
    id: "fanqie",
    name: "番茄小说",
    hint: "检查可读性、原创表达、因果与信息推进，避免空洞拼凑及重复水文。",
  },
  {
    id: "qidian",
    name: "阅文 · 起点",
    hint: "编辑建议：检查核心设定的持续性、成长与代价、阶段回报和长线期待。",
  },
  {
    id: "yuewen-female",
    name: "阅文 · 女频站点",
    hint: "编辑建议：检查人物主动性、关系张力、情感代价和成长，按实际投稿站点复核。",
  },
];
export const editorialSources = [
  {
    name: "番茄签约质量要求",
    url: "https://fanqienovel.com/writer/zone/help/article?rank1=10189&rank2=10191&rank3=0",
  },
  {
    name: "番茄低质量产整治公告（2026-02-04）",
    url: "https://fanqienovel.com/writer/zone/article/7602950185735438398",
  },
  {
    name: "阅文帮助中心（需核对实际站点）",
    url: "https://help.yuewen.com/helpcenter/pc/menu?_noheader=1&siteId=2",
  },
];
export interface EditorialFinding {
  label: string;
  evidence: string;
  advice: string;
}
export function editorialFindings(text: string): EditorialFinding[] {
  const findings: EditorialFinding[] = [];
  const paragraphs = text
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter(Boolean);
  const seen = new Set<string>();
  for (const p of paragraphs) {
    if (p.length > 20 && seen.has(p))
      findings.push({
        label: "重复段落",
        evidence: p,
        advice: "确认是否有叙事作用；无作用的复述可删除或合并。",
      });
    seen.add(p);
    if ([...p].length > 450)
      findings.push({
        label: "段落较长",
        evidence: p.slice(0, 160),
        advice: "检查行动、对白和信息层次，按情节自然分段，无需机械拆句。",
      });
  }
  for (const phrase of [
    "不禁",
    "仿佛",
    "嘴角勾起",
    "命运的齿轮",
    "一股莫名",
    "殊不知",
  ]) {
    const count = text.split(phrase).length - 1;
    if (count >= 3)
      findings.push({
        label: `表达重复：${phrase}（${count}处）`,
        evidence: phrase,
        advice:
          "逐处核对是否准确必要，优先改善具体动作和因果，不做全篇同义词替换。",
      });
  }
  return findings.slice(0, 20);
}
export function editorialPrompt(
  platform: string,
  audience: string,
  focus: string,
  quote = "",
): string {
  const p =
    editorialPlatforms.find((p) => p.id === platform) ?? editorialPlatforms[0]!;
  return `任务：自然文风精修。目标：${p.name}；目标读者：${audience}；题材重点：${focus}。\n${p.hint}\n保留既定主线、主题、视角、人物身份、关键伏笔和高光；把空泛情绪落实为具体选择、行动和后果，区分人物对白。减少重复解释和模板式收尾，保持作者原有节奏。禁止靠随机换词、故意错字或AI检测规避改稿，不声称平台审核通过。专业细节无来源时列待核对。返回完整候选正文及修改理由，不能自动采用。${quote ? `\n仅精修下列选中原文及必要衔接，其他内容保持原样：\n${quote}` : "\n只修改确有问题的段落，不无故重写全章。"}`;
}
