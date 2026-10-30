import { chapterSourceSelection } from "./chapterTextTools";

export type ReferenceSource = "working" | "published";
export interface ChapterReferenceItem {
  chapter_id: string;
  control_id: string;
  title: string;
  volume_id: string;
  volume: string;
  reason: string;
}
export interface ChapterReferenceDirectory {
  book_id: string;
  items: ChapterReferenceItem[];
  complete: boolean;
  notes: string[];
}
export interface ChapterReferenceSnapshot {
  book_id: string;
  chapter_id: string;
  control_id: string;
  source: ReferenceSource;
  origin: "private" | "published";
  title: string;
  revision: number;
  source_hash: string;
  text_hash: string;
  text: string;
  characters: number;
  reason: string;
}
export interface ReferenceExcerpt {
  key: string;
  start: number;
  end: number;
  quote: string;
}
const object = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === "object" && !Array.isArray(value);
const id = (value: unknown): value is string =>
  typeof value === "string" && /^[a-f0-9]{32}$/.test(value);
const sha = (value: unknown): value is string =>
  typeof value === "string" && /^[a-f0-9]{64}$/.test(value);
export function referenceDirectory(
  value: unknown,
  book: string,
): ChapterReferenceDirectory {
  if (
    !object(value) ||
    value.book_id !== book ||
    typeof value.complete !== "boolean" ||
    !Array.isArray(value.items) ||
    value.items.length > 10000 ||
    !Array.isArray(value.notes) ||
    value.notes.length > 202 ||
    value.notes.some((note) => typeof note !== "string")
  )
    throw new Error("参考目录与当前小说不一致，请重新读取。");
  const seen = new Set<string>();
  for (const item of value.items) {
    if (
      !object(item) ||
      !id(item.chapter_id) ||
      seen.has(item.chapter_id) ||
      !id(item.volume_id) ||
      (item.control_id !== "" && !id(item.control_id)) ||
      ["title", "volume", "reason"].some((key) => typeof item[key] !== "string")
    )
      throw new Error("参考目录的章节身份不完整，请重新读取。");
    seen.add(item.chapter_id);
  }
  return value as unknown as ChapterReferenceDirectory;
}
export async function referenceSnapshot(
  value: unknown,
  book: string,
  chapter: string,
  source: ReferenceSource,
): Promise<ChapterReferenceSnapshot> {
  if (
    !object(value) ||
    value.book_id !== book ||
    value.chapter_id !== chapter ||
    value.source !== source ||
    !id(value.control_id) ||
    !["private", "published"].includes(String(value.origin)) ||
    (source === "published" && value.origin !== "published") ||
    !sha(value.source_hash) ||
    !sha(value.text_hash) ||
    !Number.isSafeInteger(value.revision) ||
    Number(value.revision) < 1 ||
    typeof value.text !== "string" ||
    !Number.isSafeInteger(value.characters) ||
    typeof value.title !== "string" ||
    typeof value.reason !== "string"
  )
    throw new Error("参考原文的作品、章节或来源不一致，请重新读取。");
  const bytes = new TextEncoder().encode(value.text);
  if (
    bytes.length > 512 * 1024 ||
    new TextDecoder().decode(bytes) !== value.text ||
    Array.from(value.text).length !== value.characters
  )
    throw new Error("参考原文超出容量或文字不完整，未展示截断内容。");
  const hash = Array.from(
    new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)),
    (byte) => byte.toString(16).padStart(2, "0"),
  ).join("");
  if (hash !== value.text_hash)
    throw new Error("参考原文摘要不一致，请重新读取。");
  return value as unknown as ChapterReferenceSnapshot;
}
export function referenceKey(value: ChapterReferenceSnapshot): string {
  return JSON.stringify([
    value.book_id,
    value.chapter_id,
    value.control_id,
    value.source,
    value.origin,
    value.revision,
    value.source_hash,
    value.text_hash,
  ]);
}
export function referenceExcerpt(
  value: ChapterReferenceSnapshot,
  start: number,
  end: number,
): ReferenceExcerpt {
  const raw = chapterSourceSelection(value.text, start, end);
  const quote = value.text.slice(raw.start, raw.end);
  if (!quote || Array.from(quote).length > 1500)
    throw new Error("参考选段最多1500字，请缩小选择范围。");
  return { key: referenceKey(value), start: raw.start, end: raw.end, quote };
}
export function referenceInstruction(
  value: ChapterReferenceSnapshot,
  excerpt: ReferenceExcerpt,
): string {
  if (
    excerpt.key !== referenceKey(value) ||
    value.text.slice(excerpt.start, excerpt.end) !== excerpt.quote ||
    !excerpt.quote ||
    Array.from(excerpt.quote).length > 1500
  )
    throw new Error("参考选段已变化，请重新选择。");
  return `作者参考：${value.title}\n章节：${value.chapter_id}\n来源：${value.origin === "private" ? "当前作者已保存私人稿" : "已采用稿"}，修订${value.revision}\n来源摘要：${value.source_hash}\n原文选段：\n${excerpt.quote}\n仅用于作者核对衔接、语气和细节；不代表当前角色知情或本章已发生事实，不得把后文信息直接写入本章。`;
}
