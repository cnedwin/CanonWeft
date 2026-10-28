export const requiredSpaceNames: Record<string, string> = {
  evidence: "原文依据",
  working: "计划与工作资料",
  methods: "方法参考",
  research: "研究候选",
  constraints: "设定约束",
};
const names: Record<string, string> = {
  snapshot: "设定内容",
  entities: "实体",
  assertions: "命题",
  rules: "规则",
  events: "事件",
  mappings: "实体对应",
  revision: "草稿版本",
  base_head: "基础版本",
  amendment: "修订已锁定内容",
  amendment_reason: "修订理由",
  space: "知识空间",
  reveal_at: "可获知时间",
  id: "记录编号",
  kind: "类型",
  name: "名称",
  aliases: "别名",
  description: "描述",
  status: "状态",
  profile: "能力设定",
  locked: "锁定",
  subject: "对象",
  predicate: "属性",
  value: "内容",
  object_id: "关联对象",
  effective_from: "生效时间",
  effective_until: "失效时间",
  known_from: "获知时间",
  known_by: "知情者",
  visibility: "可见范围",
  recorded_at: "记录时间",
  source: "来源",
  holder_id: "陈述或信念持有者",
  context: "叙述语境",
  confidence: "置信度",
  when: "适用条件",
  exceptions: "例外",
  operator: "比较方式",
  operand: "比较值",
  time: "故事时间",
  participants: "参与者",
  causes: "前因",
  requirements: "前置条件",
  effects: "效果",
  costs: "代价",
  limits: "限制",
  counters: "克制",
  growth: "成长",
  related: "关联设定",
  target: "对应对象",
  sources: "原始对象",
  reason: "理由",
};
function readable(value: unknown): string {
  if (value === null) return "未设置";
  if (typeof value === "boolean") return value ? "是" : "否";
  if (Array.isArray(value)) return value.map(readable).join("；");
  if (typeof value === "object")
    return Object.entries(value)
      .map(([key, item]) => `${names[key] ?? key}：${readable(item)}`)
      .join("\n");
  return String(value);
}
export function requiredText(kind: string, text: string): string {
  if (kind === "resource") return text;
  try {
    const value: unknown = JSON.parse(text);
    if (kind === "skill" && Array.isArray(value)) {
      return value
        .map(
          (file: { path: string; text?: string; binary?: boolean }) =>
            `${file.path}\n${file.binary ? "非文字附件，已记录原文件校验值；未解析内容。" : (file.text ?? "")}`,
        )
        .join("\n\n");
    }
    return readable(value);
  } catch {
    return text;
  }
}
