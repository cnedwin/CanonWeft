export interface ResearchSource {
  id: string;
  name: string;
  revision: string;
  hash: string;
  url: string;
  locator: string;
  captured_at: string;
}
export interface ResearchCitation {
  source_id: string;
  url: string;
  locator: string;
  captured_at: string;
  evidence: string;
}
export interface ResearchFact {
  id: string;
  approved: boolean;
  claim: string;
  status: string;
  sources: ResearchCitation[];
  stale_sources?: ResearchCitation[];
  era_scope: string;
  region_scope: string;
  limitations: string;
  writing_use: string;
  review_after: string;
}
export interface ResearchDocument {
  withdrawn_sources?: { source: ResearchSource; reason: string }[];
  title: string;
  question: string;
  genre: string;
  era: string;
  region: string;
  chapter_need: string;
  source_ids: string[];
  sources: ResearchSource[];
  generation_id?: string;
  search_plan: { query: string; purpose: string; preferred_source: string }[];
  fact_cards: ResearchFact[];
  conflicts: string[];
  gaps: string[];
  scene_checks: string[];
}
export interface ResearchDraft {
  resource_id: string;
  revision: number;
  hash: string;
  head: string;
  document: ResearchDocument;
}
