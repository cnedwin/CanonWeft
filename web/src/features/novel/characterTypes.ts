import type { Snapshot } from "../ip/types";
export interface CharacterDesignDraft {
  resource_id: string;
  revision: number;
  base_head: string;
  ip_revision: number;
  hash: string;
  document: {
    schema: string;
    book_id: string;
    notes: string;
    genre: string;
    ids: string[];
    base: Snapshot;
    candidate: Snapshot;
    generation_id?: string;
    recipe_run_id?: string;
    source_ids?: string[];
    naming?: NamingBoard;
  };
}
export interface NameCandidate {
  parts?: Partial<
    Record<
      | "surname"
      | "given_name"
      | "courtesy_name"
      | "art_name"
      | "nickname"
      | "title"
      | "original"
      | "transliteration",
      string
    >
  >;
  id: string;
  name: string;
  pronunciation: string;
  origin: "invented" | "collected" | "adapted";
  tags: string[];
  rationale: string;
  sources: {
    source_id: string;
    url: string;
    locator: string;
    evidence: string;
    captured_at: string;
  }[];
  conflicts: string[];
  confidence: "unverified" | "supported";
  locked?: boolean;
  favorite?: boolean;
}
export interface NamingBoard {
  name_kind?: string;
  catalog?: { resource_id: string; revision: number; hash: string };
  count: number;
  culture: string;
  era: string;
  constraints: string[];
  language: string;
  material_query: string;
  generation_id?: string;
  candidates: NameCandidate[];
  gaps: string[];
  checks: string[];
}
export interface NameMaterial {
  source_import_id?: string;
  entity_id: string;
  label: string;
  label_language: string;
  kind: string;
  source_url: string;
  source_row: number;
  pronunciation?: string;
  original?: string;
  transliteration?: string;
  culture?: string;
  era?: string;
  tags?: string[];
  license?: string;
  captured_at?: string;
  evidence?: string;
  review_state?: string;
}
export interface NameCatalog {
  project_id?: string;
  branch_id?: string;
  resource_id: string;
  revision: number;
  hash: string;
  document: {
    records: NameMaterial[];
    captured_at: string;
    license: string;
    source_repository: string;
    source_commit: string;
    limitations: string[];
  };
}
export interface CharacterDesignSuggestion {
  draft: CharacterDesignDraft;
  explanation: string;
  warnings: string[];
}
