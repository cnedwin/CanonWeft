export interface DeltaOperation {
  kind: string;
  object_kind: string;
  object_id: string;
  field: string;
  before_hash: string;
  after_hash: string;
  before: unknown;
  after: unknown;
  reason: string;
  source_refs?: { kind: string; id: string; revision: string; hash: string }[];
}
export interface BranchDelta {
  schema: number;
  base_hash: string;
  result_hash: string;
  operations: DeltaOperation[];
}
export function deltaKey(op: DeltaOperation) {
  return `${op.object_kind}:${op.object_id}:${op.field}`;
}
export function activeReasons(
  delta: BranchDelta,
  reasons: Record<string, string>,
) {
  return Object.fromEntries(
    delta.operations.map((op) => [deltaKey(op), reasons[deltaKey(op)] ?? ""]),
  );
}
