import { APIError } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
import type { ManuscriptChapter } from "./manuscriptTypes";

export interface EditionChapterText {
  book_id: string;
  edition_id: string;
  edition_hash: string;
  chapter: ManuscriptChapter;
  text: string;
  text_hash: string;
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function sha(value: unknown): value is string {
  return typeof value === "string" && /^[a-f0-9]{64}$/.test(value);
}

export function useEditionChapterReads(bookId: string) {
  const reads = useBoundedReads({
    stopped: "旧稿读取已停止，当前手稿保持原样。",
    timeout: "旧稿读取超时，当前手稿保持原样，可以重新读取。",
  });
  async function read(
    editionId: string,
    chapterId: string,
    expected?: ManuscriptChapter,
  ): Promise<EditionChapterText> {
    const value = await reads.read<unknown>(
      `/novels/${encodeURIComponent(bookId)}/export/editions/${encodeURIComponent(editionId)}/chapters/${encodeURIComponent(chapterId)}`,
    );
    if (
      !record(value) ||
      value.book_id !== bookId ||
      value.edition_id !== editionId ||
      !sha(value.edition_hash) ||
      !sha(value.text_hash) ||
      typeof value.text !== "string" ||
      !record(value.chapter) ||
      value.chapter.id !== chapterId ||
      value.chapter.included !== true ||
      ![
        "resource_id",
        "volume_id",
        "volume",
        "title",
        "hash",
        "head",
        "warning",
      ].every(
        (key) =>
          typeof (value.chapter as Record<string, unknown>)[key] === "string",
      ) ||
      !Number.isSafeInteger(value.chapter.revision) ||
      Number(value.chapter.revision) < 0 ||
      !Number.isSafeInteger(value.chapter.characters) ||
      Number(value.chapter.characters) < 0
    )
      throw new APIError(
        "读取的旧稿与所选作品、稿本或章节不一致，请重新读取。",
        0,
      );
    if (
      expected &&
      ["id", "resource_id", "hash", "head", "revision", "included"].some(
        (key) =>
          value.chapter &&
          (value.chapter as Record<string, unknown>)[key] !==
            expected[key as keyof ManuscriptChapter],
      )
    )
      throw new APIError("旧稿正文与刚才的目录版本不一致，请重新比较稿本。", 0);
    const hash = Array.from(
      new Uint8Array(
        await crypto.subtle.digest(
          "SHA-256",
          new TextEncoder().encode(value.text),
        ),
      ),
      (byte) => byte.toString(16).padStart(2, "0"),
    ).join("");
    if (
      hash !== value.text_hash ||
      Array.from(value.text).length !== value.chapter.characters
    )
      throw new APIError("旧稿正文校验不一致，请重新读取。", 0);
    return value as unknown as EditionChapterText;
  }
  return { read, active: reads.active };
}
