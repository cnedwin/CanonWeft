export interface SemanticCounts {
  cases: number;
  defects: number;
  allowed: number;
  observed: number;
  true_positive: number;
  false_positive: number;
  true_negative: number;
  false_negative: number;
  invalid: number;
  abstained: number;
  strict_passed: number;
  supported_defects: number;
  precision: number;
  precision_defined: boolean;
  recall: number;
  recall_defined: boolean;
  strict_accuracy: number;
  supported_recall: number;
}

export interface NovelSemanticSummary {
  suite_hash: string;
  threshold: number;
  annotation: string;
  boundary: string;
  arms: (SemanticCounts & {
    arm: string;
    passed: boolean;
    pending: number;
    duplicate_steps: number;
    unknown_steps: number;
    unsettled: number;
    breakdown: (SemanticCounts & { kind: string; name: string })[];
  })[];
}
