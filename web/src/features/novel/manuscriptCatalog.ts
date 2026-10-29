import type { ManuscriptChapter, ManuscriptPreview } from "./manuscriptTypes";

export interface ChapterReadiness {
  id: string;
  title: string;
  ready: boolean;
  reason: string;
}
export interface CatalogChapter {
  current: ManuscriptChapter;
  adopted: ManuscriptChapter;
  ready: boolean;
  reason: string;
  privateDraft: boolean;
  different: boolean;
  review: boolean;
  openable: boolean;
  position: number;
}
export type CatalogFilter =
  | "all"
  | "written"
  | "draft"
  | "adopted"
  | "empty"
  | "review";

export function manuscriptCatalog(
  current: ManuscriptPreview,
  adopted: ManuscriptPreview,
  readiness: ChapterReadiness[],
): CatalogChapter[] {
  const signature = (chapters: ManuscriptChapter[]) =>
    JSON.stringify(chapters.map((c) => [c.id, c.volume_id]));
  if (
    current.source !== "draft" ||
    adopted.source !== "published" ||
    signature(current.chapters) !== signature(adopted.chapters) ||
    !Array.isArray(readiness) ||
    readiness.some(
      (c) =>
        typeof c.id !== "string" ||
        typeof c.ready !== "boolean" ||
        typeof c.reason !== "string",
    ) ||
    JSON.stringify(current.chapters.map((c) => c.id)) !==
      JSON.stringify(readiness.map((c) => c.id))
  )
    throw new Error(
      "目录在读取期间发生变化或未完整返回，请刷新后查看，原稿未改动。",
    );
  return current.chapters.map((chapter, index) => {
    const published = adopted.chapters[index];
    const detail = readiness[index];
    const privateDraft = chapter.revision > 0;
    return {
      current: chapter,
      adopted: published,
      ready: detail.ready,
      reason: detail.reason,
      privateDraft,
      different:
        privateDraft &&
        (!published.included || chapter.hash !== published.hash),
      review:
        !!chapter.resource_id &&
        (!detail.ready || (chapter.included && !!chapter.warning)),
      openable: !!chapter.resource_id || detail.ready,
      position: index + 1,
    };
  });
}

export function filterCatalog(
  rows: CatalogChapter[],
  query: string,
  filter: CatalogFilter,
  volume: string,
) {
  const needle = query.trim().toLocaleLowerCase();
  return rows.filter((row) => {
    const chapter = row.current;
    if (volume && chapter.volume_id !== volume) return false;
    if (filter === "written" && !chapter.included) return false;
    if (filter === "draft" && !row.privateDraft) return false;
    if (filter === "adopted" && !row.adopted.included) return false;
    if (filter === "empty" && chapter.included) return false;
    if (filter === "review" && !row.review) return false;
    return (
      !needle ||
      [chapter.title, chapter.volume, chapter.id].some((text) =>
        text.toLocaleLowerCase().includes(needle),
      )
    );
  });
}
