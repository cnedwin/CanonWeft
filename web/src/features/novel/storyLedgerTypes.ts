import type { ChapterEvidenceTarget } from "./chapterEvidence";

export type { LedgerEntry } from "./chapterTypes";

export interface StoryLedgerEditTarget extends ChapterEvidenceTarget {
  control_id: string;
  ledger_id: string;
  ledger_hash: string;
  entry_id: string;
  open_ledger?: boolean;
}
