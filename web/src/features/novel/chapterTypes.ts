import type { DependencyRef } from "../ip/dependencies";
import type { PlanResource } from "./planningTypes";

export interface ChapterDocument {
  unplanned?: boolean;
  references?: ChapterReference[];
  schema: number;
  chapter_id: string;
  body_id: string;
  detail: DependencyRef;
  settings_revision: number;
  settings_hash: string;
  ip_head: string;
  story_time: number;
  perspective: string;
  title: string;
  text: string;
  writer_generation_id?: string;
}
export interface ChapterReference {
  source: DependencyRef;
  quote: string;
  use: string;
  era: string;
  region: string;
  limitations: string;
}
export interface ChapterDraft {
  resource: PlanResource;
  revision: number;
  base_head: string;
  hash: string;
  document: ChapterDocument;
  ready: boolean;
  reason: string;
  notes?: string[];
}

export interface LedgerEntry {
  id: string;
  kind: string;
  context: string;
  subject_id: string;
  subject_name: string;
  holder_id: string;
  object_id: string;
  predicate: string;
  statement: string;
  story_time: number;
  known_from: number;
  visibility: string;
  known_by: string[];
  quotes: string[];
  evidence: { start_rune: number; end_rune: number; quote: string }[];
}
export interface LedgerDraft {
  resource: PlanResource;
  revision: number;
  base_head: string;
  hash?: string;
  ready: boolean;
  reason: string;
  document: {
    schema: number;
    source: {
      book_id: string;
      resource_id: string;
      revision: number;
      hash: string;
      base_head: string;
    };
    chapter_id: string;
    body_id: string;
    text_hash: string;
    generation_id?: string;
    summary: string;
    entries: LedgerEntry[];
    notes: string[];
  };
}
