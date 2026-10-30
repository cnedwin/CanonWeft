import { useBoundedReads } from "../../composables/useBoundedReads";
import {
  referenceDirectory,
  referenceSnapshot,
  type ReferenceSource,
} from "./chapterReference";

export function useChapterReferenceReads() {
  const reads = useBoundedReads({
    stopped: "跨章参考读取已停止。",
    timeout: "跨章参考读取超时，当前手稿和精修输入保持原样，可以重试。",
  });
  return {
    active: reads.active,
    async directory(book: string) {
      return referenceDirectory(
        await reads.read<unknown>(
          `/novels/${encodeURIComponent(book)}/chapter-references`,
        ),
        book,
      );
    },
    async chapter(book: string, chapter: string, source: ReferenceSource) {
      return referenceSnapshot(
        await reads.read<unknown>(
          `/novels/${encodeURIComponent(book)}/chapter-references/${encodeURIComponent(chapter)}?source=${source}`,
        ),
        book,
        chapter,
        source,
      );
    },
  };
}
