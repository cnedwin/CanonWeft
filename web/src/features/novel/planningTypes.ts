import type { DependencyRef } from "../ip/dependencies";
export type PlanKind = "world" | "book" | "volume" | "detail";
export interface PlannedVolume {
  id: string;
  title: string;
  summary: string;
}
export interface PlannedChapter extends PlannedVolume {
  perspective: string;
  goal: string;
  events: string;
  change: string;
  transition: string;
}
export interface PlannedScene {
  id: string;
  time: string;
  place: string;
  characters: string;
  knowledge: string;
  goal: string;
  conflict: string;
  action: string;
  outcome: string;
  emotion: string;
  foreshadow: string;
  prerequisites: string;
  hook: string;
  words: number;
}
export interface PlanningDocument {
  schema: number;
  kind: PlanKind;
  scope: string;
  settings_revision: number;
  settings_hash: string;
  ip_head: string;
  inputs: DependencyRef[];
  title: string;
  summary: string;
  mainline: string;
  theme: string;
  world_rules: string;
  costs: string;
  boundaries: string;
  setting: string;
  factions: string;
  characters: string;
  arcs: string;
  turning_points: string;
  ending: string;
  open_questions: string;
  volumes: PlannedVolume[] | null;
  chapters: PlannedChapter[] | null;
  scenes: PlannedScene[] | null;
}
export interface PlanResource {
  id: string;
  name: string;
  kind: string;
  head: string;
  revision: number;
}
export interface PlanDraft {
  resource: PlanResource;
  revision: number;
  base_head: string;
  base_needs_save?: boolean;
  hash: string;
  document: PlanningDocument;
  published?: PlanningDocument;
  ready: boolean;
  reason: string;
}
export interface PlanningOverview {
  stage: string;
  reason: string;
  resources: PlanResource[];
  sources: PlanResource[];
}
export interface PlanRelease {
  id: string;
  hash: string;
  status: string;
  manifest: { artifacts: { resource_id: string }[] };
  contents?: { resource_id: string; text: string }[];
}
export const planNames: Record<PlanKind, string> = {
  world: "世界与故事",
  book: "全书大纲",
  volume: "分卷章节安排",
  detail: "章节细纲",
};
export interface PlanningSuggestion {
  draft: PlanDraft;
  explanation: string;
  questions: string[];
}
type TextKey =
  | "title"
  | "summary"
  | "mainline"
  | "theme"
  | "world_rules"
  | "costs"
  | "boundaries"
  | "setting"
  | "factions"
  | "characters"
  | "arcs"
  | "turning_points"
  | "ending"
  | "open_questions";
export const planFields: { key: TextKey; label: string; kinds: PlanKind[] }[] =
  [
    {
      key: "title",
      label: "规划标题",
      kinds: ["world", "book", "volume", "detail"],
    },
    {
      key: "summary",
      label: "内容摘要",
      kinds: ["world", "book", "volume", "detail"],
    },
    {
      key: "mainline",
      label: "主线目标与因果",
      kinds: ["world", "book", "volume"],
    },
    { key: "theme", label: "核心主题", kinds: ["world", "book"] },
    { key: "world_rules", label: "世界规则", kinds: ["world"] },
    { key: "costs", label: "能力与行动的代价", kinds: ["world"] },
    { key: "boundaries", label: "世界边界与不可违背事项", kinds: ["world"] },
    { key: "setting", label: "时空、地点与社会背景", kinds: ["world"] },
    { key: "factions", label: "势力及其冲突", kinds: ["world"] },
    { key: "characters", label: "主要人物与关系", kinds: ["world"] },
    { key: "arcs", label: "人物目标、阻碍与成长弧线", kinds: ["world"] },
    { key: "turning_points", label: "重大转折与高光时刻", kinds: ["world"] },
    { key: "ending", label: "结局方向", kinds: ["world", "book"] },
    {
      key: "open_questions",
      label: "有意保留的未知与待定事项",
      kinds: ["world", "book", "volume", "detail"],
    },
  ];
export const chapterFields: {
  key: Exclude<keyof PlannedChapter, "id">;
  label: string;
}[] = [
  { key: "title", label: "章节标题" },
  { key: "summary", label: "章节摘要" },
  { key: "perspective", label: "叙事视角" },
  { key: "goal", label: "本章目标" },
  { key: "events", label: "关键事件" },
  { key: "change", label: "人物状态变化" },
  { key: "transition", label: "前后衔接" },
];
export const sceneFields: {
  key: Exclude<keyof PlannedScene, "id" | "words">;
  label: string;
}[] = [
  { key: "time", label: "故事时间" },
  { key: "place", label: "场景地点" },
  { key: "characters", label: "在场人物" },
  { key: "knowledge", label: "视角可知信息" },
  { key: "goal", label: "场景目标" },
  { key: "conflict", label: "场景冲突" },
  { key: "action", label: "人物行动" },
  { key: "outcome", label: "行动结果" },
  { key: "emotion", label: "情绪变化" },
  { key: "foreshadow", label: "伏笔与回收" },
  { key: "prerequisites", label: "因果前置" },
  { key: "hook", label: "场景结尾与钩子" },
];
export const planningID = () => crypto.randomUUID().replaceAll("-", "");
