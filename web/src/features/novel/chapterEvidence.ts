export interface ChapterEvidenceTarget {
  chapter_id: string;
  quote: string;
  reviewed_hash: string;
  title: string;
  explanation: string;
  suggestion: string;
  // Unicode code-point offsets from the saved source, never textarea offsets.
  match_start?: number;
  match_end?: number;
}

export function chapterEvidencePosition(
  text: string,
  target: ChapterEvidenceTarget,
  hash: string,
  saved: boolean,
): { start: number; end: number } | null {
  const start = target.match_start,
    end = target.match_end;
  if (
    !saved ||
    !target.reviewed_hash ||
    hash !== target.reviewed_hash ||
    !Number.isSafeInteger(start) ||
    !Number.isSafeInteger(end) ||
    start === undefined ||
    end === undefined ||
    start < 0 ||
    end <= start ||
    !target.quote
  )
    return null;
  const points = Array.from(text);
  if (end > points.length || points.slice(start, end).join("") !== target.quote)
    return null;
  return {
    start: points.slice(0, start).join("").length,
    end: points.slice(0, end).join("").length,
  };
}
