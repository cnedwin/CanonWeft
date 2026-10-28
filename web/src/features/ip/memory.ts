export const memoryKinds = {
  theme: "主题",
  mainline: "主线",
  highlight: "高光",
  summary: "摘要",
  relation: "关系",
  short_term: "短期记忆",
  fact: "事实",
  belief: "人物信念",
  claim: "对外陈述",
  intent: "意图",
  hypothesis: "推测",
  event: "事件",
  rule: "世界规则",
  promise: "伏笔与承诺",
  state: "阶段状态",
  professional: "专业参考",
  method: "创作方法",
} as const;
export type MemoryKind = keyof typeof memoryKinds;
export const summaryLevels = {
  scene: "场景",
  chapter: "章节",
  volume: "分卷",
  book: "全书",
} as const;
export type SummaryLevel = keyof typeof summaryLevels;
export interface MemoryRefreshTask {
  id: string;
  state: "READY" | "WAITING_INPUT" | "REVIEW";
  reason: string;
  input: {
    resource_ids?: string[] | null;
    memory_ids?: string[] | null;
    story_time: number;
    summary_target: {
      item_id: string;
      revision: string;
      level: SummaryLevel;
      unit_id: string;
      title: string;
      timeline: string;
    };
  };
}
export interface SummaryPin {
  id: string;
  revision: string;
  hash: string;
}
export type MemoryPurpose = "author" | "writer" | "critic" | "research";
export type MemoryState =
  | "published"
  | "adopted_plan"
  | "candidate"
  | "working"
  | "stale"
  | "revoked";
export interface MemorySource {
  resource: string;
  revision: string;
  hash: string;
  start_rune: number;
  end_rune: number;
}
export interface MemoryItem {
  summary?: {
    level: SummaryLevel;
    unit_id: string;
    title: string;
    inputs?: SummaryPin[];
  };
  id: string;
  revision: string;
  project: string;
  branch: string;
  actor?: string;
  session?: string;
  timeline?: string;
  save?: string;
  kind: MemoryKind;
  state: MemoryState;
  visibility: "author" | "public" | "restricted";
  known_by?: string[];
  from: number;
  until?: number | null;
  reveal_at: number;
  control?: boolean;
  pinned?: boolean;
  text: string;
  sources: MemorySource[];
  dependencies?: string[];
  score: number;
}
export interface MemorySessionDocument {
  title: string;
  goal: string;
  notes: string;
  focus_ids: string[];
}
export interface MemorySession {
  retention?: { state: "EXPIRED"; expired_at: string };
  id: string;
  revision: number;
  document: MemorySessionDocument;
}
export interface MemoryResource {
  id: string;
  name: string;
  head: string;
}
export interface MemoryResourceDetail extends MemoryResource {
  hash: string;
  text: string;
}
export interface MemoryPack {
  request: {
    scope: {
      project: string;
      branch: string;
      canon: string;
      actor: string;
      permission_epoch: string;
      purpose: MemoryPurpose;
      perspective: string;
      story_time: number;
      narrative_time: number;
      session: string;
      timeline: string;
    };
    required_ids?: string[];
    budget: number;
    output_reserve: number;
    overhead_reserve: number;
    source_epoch: string;
    compiler_version: string;
  };
  items: { item: MemoryItem; channel: string; reason: string }[];
  omissions: { id: string; reason: string }[];
  omitted_count: number;
  estimated_tokens: number;
  hash?: string;
}
export const memoryStates: Record<MemoryState, string> = {
  candidate: "未确认候选",
  working: "私人工作记忆",
  published: "已采用的记忆",
  adopted_plan: "已采用的创作约束",
  stale: "来源已过期",
  revoked: "已撤回",
};
export function copyMemory<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}
export function locateMemorySource(
  resource: MemoryResourceDetail,
  excerpt: string,
): MemorySource {
  if (!resource.head || !/^[a-f0-9]{64}$/.test(resource.hash))
    throw new Error("请选择有已确认版本的来源资料。");
  if (!excerpt.trim()) throw new Error("请选中或填写需引用的原文片段。");
  const start = resource.text.indexOf(excerpt);
  if (start < 0) throw new Error("当前已确认原文中找不到这个片段。");
  if (resource.text.indexOf(excerpt, start + 1) >= 0)
    throw new Error("片段出现多次，请选择更长的原文以准确定位。");
  const first = Array.from(resource.text.slice(0, start)).length;
  return {
    resource: resource.id,
    revision: resource.head,
    hash: resource.hash,
    start_rune: first,
    end_rune: first + Array.from(excerpt).length,
  };
}
