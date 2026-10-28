export interface NovelImageTarget {
  book_id: string;
  kind: "cover" | "scene";
  name: string;
  source_ids: string[];
  character_ids: string[];
}
export interface VisualImageChoice {
  id: string;
  report: {
    character_image?: {
      character_id?: string;
      look: string;
      view: string;
      width: number;
      height: number;
    };
    novel_image?: {
      book_id: string;
      name: string;
      kind: string;
      width: number;
      height: number;
    };
  };
}
export function visualDimensions(item: VisualImageChoice) {
  const value = item.report.novel_image ?? item.report.character_image;
  return { width: value?.width ?? 0, height: value?.height ?? 0 };
}
export function visualLabel(item: VisualImageChoice) {
  const novel = item.report.novel_image;
  if (novel)
    return `${novel.kind === "cover" ? "封面" : "场景"} · ${novel.name}`;
  const character = item.report.character_image;
  return character ? `${character.look} · ${character.view}` : "参考图片";
}
