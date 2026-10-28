export interface Entity {
  id: string;
  kind: string;
  name: string;
  aliases: string[];
  description: string;
  status?: string;
  locked?: boolean;
  profile?: CapabilityProfile;
}
export interface CapabilityProfile {
  requirements: {
    subject: string;
    predicate: string;
    operator: string;
    operand: string;
  }[];
  effects: string[];
  costs: string[];
  limits: string[];
  counters: string[];
  growth: string[];
  related: string[];
  source: string;
}
export interface EntityMapping {
  id: string;
  kind: string;
  target: string;
  sources: string[];
  reason: string;
  source: string;
}
export interface Assertion {
  id: string;
  subject: string;
  predicate: string;
  value: string;
  object_id?: string;
  kind: string;
  effective_from: number;
  known_from: number;
  known_by: string[];
  visibility: string;
  recorded_at: string;
  locked: boolean;
  source: string;
  holder_id?: string;
  context?: string;
  status?: string;
  confidence?: number | null;
  effective_until?: number | null;
}
export interface Snapshot {
  entities: Entity[];
  assertions: Assertion[];
  rules?: WorldRule[];
  events?: StoryEvent[];
  mappings?: EntityMapping[];
}
export interface WorldRule {
  when?: PredicateTest[];
  exceptions?: RuleException[];
  effective_from?: number | null;
  effective_until?: number | null;
  id: string;
  name: string;
  subject: string;
  predicate: string;
  operator: string;
  operand: string;
  source: string;
  locked: boolean;
  recorded_at: string;
}
export interface PredicateTest {
  subject: string;
  predicate: string;
  operator: string;
  operand: string;
}
export interface RuleException {
  id: string;
  name: string;
  source: string;
  when: PredicateTest[];
}
export interface StoryEvent {
  id: string;
  name: string;
  description: string;
  time: number;
  participants: string[];
  causes: string[];
  source: string;
  locked: boolean;
  recorded_at: string;
}
export interface IPDraft {
  branch_actions?: BranchActionDraft;
  change_reasons?: Record<string, string>;
  revision: number;
  base_head: string;
  snapshot: Snapshot;
  amendment: boolean;
  amendment_reason: string;
}
export interface PromotionDraft {
  source_branch: string;
  source_head: string;
  base_head: string;
  operation_id: string;
  selection: {
    entities: string[];
    assertions: string[];
    rules: string[];
    events: string[];
  };
  reason: string;
  change_reasons: Record<string, string>;
}
export interface BranchActionDraft {
  merge?: {
    base_head: string;
    upstream_head: string;
    operation_id: string;
    resolutions: Record<string, string>;
    change_reasons: Record<string, string>;
  };
  promotion?: PromotionDraft;
}
export interface MergeView {
  base_head: string;
  upstream_head: string;
  snapshot: Snapshot;
  base: Snapshot;
  upstream: Snapshot;
  local: Snapshot;
  conflicts: { type: string; assertion_id: string; reason: string }[];
}
export interface Branch {
  id: string;
  name: string;
  parent_id: string;
  base_revision: string;
  head: string;
  revision: number;
  snapshot: Snapshot;
}
export interface Candidate {
  retention?: { state: "EXPIRED"; expired_at: string };
  delta?: import("./branchDelta").BranchDelta;
  requires_semantic_review?: boolean;
  promotion?: { source_branch: string; source_head: string; reason: string };
  id: string;
  branch_id: string;
  base_head: string;
  hash: string;
  status: "PREPARED" | "READY" | "PUBLISHED";
  snapshot: Snapshot;
  amendment_reason: string;
  checks: {
    rule_id: string;
    status: string;
    reason: string;
    story_time: number;
  }[];
}
