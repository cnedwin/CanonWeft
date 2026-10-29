import { APIError } from "../../api";
import type { LedgerEntry } from "./chapterTypes";

export const ledgerKinds: Record<string, string> = {
  fact: "事实",
  belief: "信念",
  claim: "陈述/传闻",
  intent: "意图",
  hypothesis: "假设",
  event: "事件",
  relation: "关系",
  state: "状态",
  promise: "承诺/伏笔",
  highlight: "高光",
  summary: "摘要",
};
export const ledgerContexts: Record<string, string> = {
  reality: "现实",
  flashback: "回忆",
  dream: "梦境",
  hypothetical: "假设语境",
  plan: "未发生的计划",
};
export const ledgerOrigins: Record<string, string> = {
  private: "本人保存的私人账本",
  published: "正式采用账本",
  unavailable: "账本不可读或尚未保存",
};
export interface StoryLedgerItem {
  chapter_id: string;
  control_id: string;
  ledger_id: string;
  chapter_title: string;
  origin: string;
  ledger_revision: number;
  ledger_hash: string;
  source_hash: string;
  current_hash: string;
  current_revision: number;
  current_ready: boolean;
  source_matches_current: boolean;
  reason: string;
  summary: string;
  entries: LedgerEntry[];
  people: { id: string; name: string }[];
}
export interface StoryLedgerPage {
  book_id: string;
  source: string;
  items: StoryLedgerItem[];
  next_cursor: string;
  scanned: number;
  notes: string[];
}
export interface StoryLedgerView {
  source: "working" | "published";
  query: string;
  person: string;
  kind: string;
  context: string;
  origin: string;
  freshness: string;
  minimum: string;
  maximum: string;
  order: string;
  focusedLedger: string;
  focusedEntry: string;
  focusedHash: string;
  pages: number;
  startCursor: string;
}
export function initialStoryLedgerView(): StoryLedgerView {
  return {
    source: "working",
    query: "",
    person: "",
    kind: "",
    context: "",
    origin: "",
    freshness: "",
    minimum: "",
    maximum: "",
    order: "chapter",
    focusedLedger: "",
    focusedEntry: "",
    focusedHash: "",
    pages: 1,
    startCursor: "",
  };
}
function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function strings(value: unknown): value is string[] {
  return (
    Array.isArray(value) && value.every((item) => typeof item === "string")
  );
}
function natural(value: unknown): value is number {
  return Number.isSafeInteger(value) && Number(value) >= 0;
}
function validEntry(value: unknown): boolean {
  return (
    record(value) &&
    [
      "id",
      "kind",
      "context",
      "subject_id",
      "subject_name",
      "holder_id",
      "object_id",
      "predicate",
      "statement",
      "visibility",
    ].every((key) => typeof value[key] === "string") &&
    !!value.id &&
    typeof value.kind === "string" &&
    !!ledgerKinds[value.kind] &&
    typeof value.context === "string" &&
    !!ledgerContexts[value.context] &&
    natural(value.story_time) &&
    natural(value.known_from) &&
    strings(value.known_by) &&
    strings(value.quotes) &&
    (value.evidence === undefined ||
      (Array.isArray(value.evidence) &&
        value.evidence.every(
          (e) =>
            record(e) &&
            natural(e.start_rune) &&
            natural(e.end_rune) &&
            e.end_rune >= e.start_rune &&
            typeof e.quote === "string",
        )))
  );
}
export function storyLedgerPage(
  value: unknown,
  book: string,
  source: string,
): StoryLedgerPage {
  if (!record(value) || value.book_id !== book || value.source !== source)
    throw new APIError("返回的作品或账本来源不一致，请重新读取。", 0);
  if (
    !Array.isArray(value.items) ||
    value.items.length > 5 ||
    !natural(value.scanned) ||
    value.scanned > 5 ||
    typeof value.next_cursor !== "string" ||
    !strings(value.notes)
  )
    throw new APIError("账本页未完整返回，请重新读取。", 0);
  const ids = new Set<string>();
  for (const item of value.items) {
    if (
      !record(item) ||
      ![
        "chapter_id",
        "control_id",
        "ledger_id",
        "chapter_title",
        "origin",
        "ledger_hash",
        "source_hash",
        "current_hash",
        "reason",
        "summary",
      ].every((key) => typeof item[key] === "string") ||
      !item.ledger_id ||
      ids.has(String(item.ledger_id)) ||
      !ledgerOrigins[String(item.origin)] ||
      !natural(item.ledger_revision) ||
      !natural(item.current_revision) ||
      typeof item.current_ready !== "boolean" ||
      typeof item.source_matches_current !== "boolean" ||
      !Array.isArray(item.entries) ||
      item.entries.length > 128 ||
      !item.entries.every(validEntry) ||
      !Array.isArray(item.people) ||
      !item.people.every(
        (p) =>
          record(p) && typeof p.id === "string" && typeof p.name === "string",
      )
    )
      throw new APIError("账本条目或来源未完整返回，请重新读取。", 0);
    ids.add(String(item.ledger_id));
    if (source === "published" && item.origin === "private")
      throw new APIError("正式账本返回了私人来源，请重新读取。", 0);
    if (
      new Set(item.entries.map((e) => (e as Record<string, unknown>).id))
        .size !== item.entries.length
    )
      throw new APIError("账本条目编号重复，未展示不明确的结果。", 0);
  }
  return value as unknown as StoryLedgerPage;
}
export function ledgerPeople(entry: LedgerEntry): string[] {
  return [
    ...new Set(
      [
        entry.subject_id,
        entry.holder_id,
        entry.object_id,
        ...entry.known_by,
      ].filter(Boolean),
    ),
  ];
}
