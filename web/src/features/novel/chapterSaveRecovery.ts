import type { ChapterDocument, ChapterDraft } from "./chapterTypes";

export interface ChapterSaveAttempt {
  id: number;
  resourceId: string;
  revision: number;
  hash: string;
  baseHead: string;
  previous: ChapterDocument;
  document: ChapterDocument;
  content: string;
}
export interface ChapterSaveResolution {
  kind: "committed" | "retry";
  attemptId: number;
  remote: ChapterDraft;
  currentDocument: string;
}

function ordered(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(ordered);
  if (value && typeof value === "object")
    return Object.fromEntries(
      Object.entries(value)
        .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
        .map(([key, item]) => [key, ordered(item)]),
    );
  return value;
}
export function sameChapterDocument(
  left: ChapterDocument,
  right: ChapterDocument,
) {
  const normalize = (value: ChapterDocument) =>
    ordered({
      ...value,
      references: value.references ?? [],
      writer_generation_id: value.writer_generation_id ?? "",
    });
  return JSON.stringify(normalize(left)) === JSON.stringify(normalize(right));
}

export function classifyChapterSave(
  attempt: ChapterSaveAttempt,
  remote: ChapterDraft,
): "committed" | "unchanged" | "diverged" {
  if (
    !remote ||
    remote.resource?.id !== attempt.resourceId ||
    !Number.isSafeInteger(remote.revision) ||
    typeof remote.hash !== "string" ||
    !remote.hash ||
    remote.base_head !== attempt.baseHead ||
    !remote.document ||
    remote.document.chapter_id !== attempt.document.chapter_id ||
    remote.document.body_id !== attempt.document.body_id ||
    typeof remote.document.text !== "string"
  )
    return "diverged";
  if (
    remote.revision === attempt.revision + 1 &&
    sameChapterDocument(remote.document, attempt.document)
  )
    return "committed";
  if (
    remote.revision === attempt.revision &&
    remote.hash === attempt.hash &&
    sameChapterDocument(remote.document, attempt.previous)
  )
    return "unchanged";
  return "diverged";
}
