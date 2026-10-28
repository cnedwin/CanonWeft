export interface DependencyRef {
  kind: string;
  branch_id: string;
  id: string;
  revision: string;
  hash: string;
}
export interface Dependency {
  source: DependencyRef;
  kind: string;
  reason: string;
  pinned?: boolean;
}
export interface DependencyPolicy {
  frozen: boolean;
  inputs: Dependency[];
}
export interface DependencyEntry {
  ref: DependencyRef;
  name: string;
  branch_name: string;
}
export const dependencyNames: Record<string, string> = {
  production: "制作输入",
  semantic: "语义关联",
  evidence: "事实证据",
  representation: "角色与场景表现",
  timeline: "时间与因果",
  capability: "能力与规则",
};
export function describeDependencies(p?: DependencyPolicy): string {
  if (!p) return "尚未记录依赖，影响覆盖待核对";
  return `${p.frozen ? "已冻结" : "可修订"} · ${p.inputs.length} 项依赖`;
}
