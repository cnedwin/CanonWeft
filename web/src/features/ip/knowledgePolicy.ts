export interface KnowledgePolicy {
  withdrawn?: boolean;
  withdrawal_reason?: string;
  space: "evidence" | "working" | "methods" | "research";
  visibility: "author" | "public" | "restricted";
  known_by?: string[];
  reveal_at: number;
}
export const spaceNames: Record<KnowledgePolicy["space"], string> = {
  evidence: "作品证据",
  working: "项目工作资料",
  methods: "专业方法参考",
  research: "外部研究候选",
};
export function defaultPolicy(): KnowledgePolicy {
  return { space: "evidence", visibility: "author", reveal_at: 0 };
}
export function describePolicy(policy?: KnowledgePolicy): string {
  const p = policy ?? defaultPolicy();
  if (p.withdrawn) return `已撤回使用 · ${p.withdrawal_reason ?? "请核对来源"}`;
  const visibility = {
    author: "仅作者与审查",
    public: "故事内公开",
    restricted: "指定角色知情",
  };
  return `${spaceNames[p.space]} · ${visibility[p.visibility]}${p.visibility !== "author" ? ` · 自故事时间 ${p.reveal_at} 起` : ""}`;
}
