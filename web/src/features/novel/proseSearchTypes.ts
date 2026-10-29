import type { ChapterEvidenceTarget } from "./chapterEvidence";

export interface ProseSearchMatch {
  start: number;
  end: number;
  quote: string;
  before: string;
  after: string;
}

export interface ProseSearchItem {
  chapter_id: string;
  control_id: string;
  chapter_title: string;
  origin: "private" | "published" | "unavailable";
  source_hash: string;
  revision: number;
  characters: number;
  total_matches: number;
  truncated: boolean;
  reason: string;
  matches: ProseSearchMatch[];
}

export interface ProseSearchPage {
  book_id: string;
  source: "working" | "published";
  query: string;
  items: ProseSearchItem[];
  next_cursor: string;
  scanned: number;
  notes: string[];
}

export interface ProseSearchEditTarget extends ChapterEvidenceTarget {
  control_id: string;
  match_start: number;
  match_end: number;
}
