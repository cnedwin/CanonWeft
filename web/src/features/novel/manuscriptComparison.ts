import type { ManuscriptChapter, ManuscriptPreview } from "./manuscriptTypes";

export type ManuscriptChange =
  | "added"
  | "removed"
  | "version"
  | "order"
  | "volume"
  | "title"
  | "availability"
  | "warning";

export const manuscriptChangeLabels: Record<ManuscriptChange, string> = {
  added: "新增章节",
  removed: "移出目录",
  version: "章节版本变化",
  order: "章节次序变化",
  volume: "分卷变化",
  title: "章名变化",
  availability: "正文纳入状态变化",
  warning: "待核对提示变化",
};

export interface ManuscriptComparisonRow {
  id: string;
  left?: ManuscriptChapter;
  right?: ManuscriptChapter;
  leftPosition?: number;
  rightPosition?: number;
  changes: ManuscriptChange[];
  charactersDelta: number;
}

export function compareManuscripts(
  left: ManuscriptPreview,
  right: ManuscriptPreview,
) {
  const before = new Map(left.chapters.map((chapter) => [chapter.id, chapter]));
  const after = new Map(right.chapters.map((chapter) => [chapter.id, chapter]));
  const beforePositions = new Map(
    left.chapters.map((chapter, index) => [chapter.id, index + 1]),
  );
  const afterPositions = new Map(
    right.chapters.map((chapter, index) => [chapter.id, index + 1]),
  );
  // Added/removed chapters shift absolute positions without reordering the
  // retained chapters. Compare their relative order to avoid false changes.
  const beforeOrder = new Map(
    left.chapters
      .filter((chapter) => after.has(chapter.id))
      .map((chapter, index) => [chapter.id, index]),
  );
  const afterOrder = new Map(
    right.chapters
      .filter((chapter) => before.has(chapter.id))
      .map((chapter, index) => [chapter.id, index]),
  );
  const ids = [
    ...right.chapters.map((chapter) => chapter.id),
    ...left.chapters
      .filter((chapter) => !after.has(chapter.id))
      .map((chapter) => chapter.id),
  ];
  const rows: ManuscriptComparisonRow[] = ids.map((id) => {
    const prior = before.get(id),
      next = after.get(id);
    const changes: ManuscriptChange[] = [];
    if (!prior) changes.push("added");
    else if (!next) changes.push("removed");
    else {
      if (prior.hash !== next.hash) changes.push("version");
      if (beforeOrder.get(id) !== afterOrder.get(id)) changes.push("order");
      if (prior.volume_id !== next.volume_id || prior.volume !== next.volume)
        changes.push("volume");
      if (prior.title !== next.title) changes.push("title");
      if (prior.included !== next.included) changes.push("availability");
      if (prior.warning !== next.warning) changes.push("warning");
    }
    return {
      id,
      left: prior,
      right: next,
      leftPosition: beforePositions.get(id),
      rightPosition: afterPositions.get(id),
      changes,
      charactersDelta:
        (next?.included ? next.characters : 0) -
        (prior?.included ? prior.characters : 0),
    };
  });
  return {
    rows,
    changed: rows.filter((row) => row.changes.length > 0).length,
    added: rows.filter((row) => row.changes.includes("added")).length,
    removed: rows.filter((row) => row.changes.includes("removed")).length,
    versions: rows.filter((row) => row.changes.includes("version")).length,
    reordered: rows.filter((row) => row.changes.includes("order")).length,
    charactersDelta: right.characters - left.characters,
  };
}
