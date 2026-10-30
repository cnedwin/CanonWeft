export interface ManuscriptTarget {
  control_id: string;
  revision: number;
  hash: string;
  base_head: string;
}
export interface ManuscriptPart {
  id: string;
  start: number;
  end: number;
  title: string;
  volume: string;
  mode: "new" | "skip" | "replace";
  target?: ManuscriptTarget;
}
export interface ManuscriptProgress {
  batch_id: string;
  part_id: string;
  status: "done" | "skipped" | "failed";
  reason: string;
  chapter_id: string;
  control_id: string;
  saved_revision: number;
  saved_hash: string;
  operation_id: string;
  revision: number;
}
export interface ManuscriptBatch {
  id: string;
  revision: number;
  hash: string;
  document: {
    schema: string;
    book_id: string;
    source_id: string;
    source_hash: string;
    text_hash: string;
    total: number;
    complete: boolean;
    accept_partial: boolean;
    parts: ManuscriptPart[];
    operation_id: string;
    created_at: string;
    recovered?: boolean;
  };
  progress: ManuscriptProgress[];
}
export interface ManuscriptSource {
  id: string;
  name: string;
  hash: string;
  size: number;
  characters: number;
  complete: boolean;
  extracted: number;
  pending: number;
  notes: string[];
}
export interface ManuscriptMatch extends ManuscriptTarget {
  chapter_id: string;
  title: string;
  exact: boolean;
}
export interface ManuscriptPreview {
  part_id: string;
  text: string;
  start: number;
  end: number;
  next: number;
  characters: number;
  bytes: number;
  importable: boolean;
  matches: ManuscriptMatch[];
  uncertain: number;
  notes: string[];
}
