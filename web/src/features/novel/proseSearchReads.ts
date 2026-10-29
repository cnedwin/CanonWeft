import { onBeforeUnmount } from "vue";
import { api, APIError } from "../../api";
import type { ProseSearchPage } from "./proseSearchTypes";

export interface ProseSearchView {
  query: string;
  source: "working" | "published";
  appliedQuery: string;
  pages: number;
  startCursor: string;
  focusedControl: string;
  focusedHash: string;
  focusedStart: number;
}
export const initialProseSearchView = (): ProseSearchView => ({
  query: "",
  source: "working",
  appliedQuery: "",
  pages: 1,
  startCursor: "",
  focusedControl: "",
  focusedHash: "",
  focusedStart: -1,
});

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}
const str = (v: unknown): v is string => typeof v === "string";
const integer = (v: unknown): v is number =>
  typeof v === "number" && Number.isSafeInteger(v) && v >= 0;

export function proseSearchPage(
  value: unknown,
  book: string,
  source: string,
  query: string,
): ProseSearchPage {
  const fail = () => {
    throw new Error("正文查找响应与当前书籍、查询或范围不匹配，未使用此结果。");
  };
  if (
    !record(value) ||
    value.book_id !== book ||
    value.source !== source ||
    value.query !== query ||
    !Array.isArray(value.items) ||
    value.items.length > 5 ||
    !integer(value.scanned) ||
    value.scanned !== value.items.length ||
    !str(value.next_cursor) ||
    !Array.isArray(value.notes) ||
    !value.notes.every(str)
  )
    return fail();
  if (new TextEncoder().encode(JSON.stringify(value)).length > 1048576)
    return fail();
  const ids = new Set<string>();
  for (const item of value.items) {
    if (
      !record(item) ||
      !str(item.control_id) ||
      !item.control_id ||
      ids.has(item.control_id) ||
      !str(item.chapter_id) ||
      !str(item.chapter_title) ||
      !str(item.source_hash) ||
      !str(item.reason) ||
      !["private", "published", "unavailable"].includes(String(item.origin)) ||
      (source === "published" && item.origin === "private") ||
      !integer(item.revision) ||
      !integer(item.characters) ||
      !integer(item.total_matches) ||
      typeof item.truncated !== "boolean" ||
      !Array.isArray(item.matches) ||
      item.matches.length > 50 ||
      item.total_matches < item.matches.length ||
      item.truncated !== item.total_matches > item.matches.length
    )
      return fail();
    ids.add(item.control_id);
    if (
      item.origin === "unavailable" &&
      (item.matches.length || item.total_matches || !item.reason)
    )
      return fail();
    if (
      item.origin !== "unavailable" &&
      (!item.chapter_id || !item.source_hash)
    )
      return fail();
    let last = -1;
    for (const hit of item.matches) {
      if (
        !record(hit) ||
        !integer(hit.start) ||
        !integer(hit.end) ||
        hit.start <= last ||
        hit.end > item.characters ||
        hit.end - hit.start !== Array.from(query).length ||
        hit.quote !== query ||
        !str(hit.before) ||
        !str(hit.after) ||
        Array.from(hit.before).length > 32 ||
        Array.from(hit.after).length > 32
      )
        return fail();
      last = hit.end - 1;
    }
  }
  return value as unknown as ProseSearchPage;
}

// This one POST is a read-only local query: unlike mutations it owns no job,
// save, lease or unknown financial outcome. Keep query text out of URLs.
export function useProseSearchReads() {
  let disposed = false;
  const controllers = new Set<AbortController>();
  function cancel() {
    for (const controller of controllers) controller.abort();
  }
  onBeforeUnmount(() => {
    disposed = true;
    cancel();
  });
  async function read(
    book: string,
    source: string,
    query: string,
    after: string,
  ) {
    if (disposed) throw new APIError("正文查找读取已停止。", 0);
    const controller = new AbortController();
    controllers.add(controller);
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, 15000);
    try {
      const value = await api<unknown>(
        `/novels/${encodeURIComponent(book)}/prose-search`,
        "POST",
        { source, query, after },
        { signal: controller.signal },
      );
      return proseSearchPage(value, book, source, query);
    } catch (error) {
      if (error instanceof APIError && error.status !== 0) throw error;
      if (timedOut)
        throw new APIError("正文查找超时，原文未改动，可以重试。", 0);
      throw error;
    } finally {
      clearTimeout(timer);
      controllers.delete(controller);
    }
  }
  return { read, cancel, active: () => !disposed };
}
