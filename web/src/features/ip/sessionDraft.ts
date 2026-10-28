import type { MemorySession, MemorySessionDocument } from "./memory";

export interface MemorySessionWrite {
  id: string;
  expected_revision: number;
  document: MemorySessionDocument;
  operation_id: string;
}
export interface MemorySessionDraft {
  schema: 1;
  base: MemorySession | null;
  document: MemorySessionDocument;
  pending: MemorySessionWrite | null;
  conflict: boolean;
  revision?: string;
  parents?: { key: string; revision: string }[];
  updated_at?: number;
}
function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function validDocument(value: unknown): value is MemorySessionDocument {
  if (!object(value)) return false;
  return (
    typeof value.title === "string" &&
    value.title.length <= 320 &&
    typeof value.goal === "string" &&
    value.goal.length <= 16000 &&
    typeof value.notes === "string" &&
    value.notes.length <= 65536 &&
    Array.isArray(value.focus_ids) &&
    value.focus_ids.length <= 128 &&
    value.focus_ids.every((id) => typeof id === "string" && id.length <= 128)
  );
}
function validSession(value: unknown): value is MemorySession {
  return (
    object(value) &&
    typeof value.id === "string" &&
    value.id.length > 0 &&
    value.id.length <= 128 &&
    Number.isSafeInteger(value.revision) &&
    Number(value.revision) > 0 &&
    validDocument(value.document)
  );
}
export function readSessionDraft(key: string): MemorySessionDraft | null {
  const raw = localStorage.getItem(key);
  if (!raw) return null;
  if (raw.length > 300000)
    throw new Error("本机工作记录暂存过大，请保留原稿后重新创建。");
  const value: unknown = JSON.parse(raw);
  if (
    !object(value) ||
    value.schema !== 1 ||
    !validDocument(value.document) ||
    (value.base !== null && !validSession(value.base)) ||
    typeof value.conflict !== "boolean"
  )
    throw new Error("本机工作记录暂存格式不完整，未自动覆盖。");
  if (value.pending !== null) {
    const pending = value.pending;
    const base = value.base as MemorySession | null;
    if (
      !object(pending) ||
      pending.id !== (base?.id ?? "") ||
      pending.expected_revision !== (base?.revision ?? 0) ||
      typeof pending.operation_id !== "string" ||
      !/^request-v2:[a-f0-9]{32}$/.test(pending.operation_id) ||
      !validDocument(pending.document) ||
      JSON.stringify(pending.document) !== JSON.stringify(value.document)
    )
      throw new Error("上次保存请求与本机文稿不一致，未自动重发。");
  }
  return value as unknown as MemorySessionDraft;
}
export interface RecoverableSessionDraft {
  key: string;
  title: string;
  updatedAt: number;
  pending: boolean;
}

// A mounted editor owns its own record, even when two windows restore the same
// draft. Parent revisions let a successful save clean only unchanged ancestors.
export class SessionDraftStore {
  private readonly key: string;
  private readonly pointer: string;
  private parents: { key: string; revision: string }[] = [];
  constructor(private readonly scope: string) {
    this.key = `${scope}:${crypto.randomUUID()}`;
    this.pointer = `${scope}:active`;
  }
  private accepts(key: string) {
    return key === this.scope || key.startsWith(`${this.scope}:`);
  }
  list(): RecoverableSessionDraft[] {
    const records: RecoverableSessionDraft[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (!key || !this.accepts(key) || key === this.key) continue;
      try {
        const draft = readSessionDraft(key);
        if (draft)
          records.push({
            key,
            title: draft.document.title || "未命名工作记录",
            updatedAt: draft.updated_at ?? 0,
            pending: !!draft.pending,
          });
      } catch {
        /* Preserve corrupt records without executing or overwriting. */
      }
    }
    return records.sort((a, b) => b.updatedAt - a.updatedAt);
  }
  read(selected?: string): MemorySessionDraft | null {
    let key = selected ?? sessionStorage.getItem(this.pointer);
    if (!key || !this.accepts(key) || !localStorage.getItem(key)) {
      const records = this.list();
      if (records.length !== 1) return null;
      key = records[0]!.key;
    }
    const draft = readSessionDraft(key);
    if (!draft) return null;
    this.parents = (Array.isArray(draft.parents) ? draft.parents : [])
      .filter(
        (parent) =>
          parent &&
          typeof parent.key === "string" &&
          this.accepts(parent.key) &&
          typeof parent.revision === "string",
      )
      .slice(-127);
    this.parents.push({ key, revision: draft.revision ?? "" });
    return draft;
  }
  write(draft: MemorySessionDraft | null) {
    if (!draft) {
      this.discard();
      return;
    }
    const value = {
      ...draft,
      revision: crypto.randomUUID(),
      parents: this.parents,
      updated_at: Date.now(),
    };
    localStorage.setItem(this.key, JSON.stringify(value));
    sessionStorage.setItem(this.pointer, this.key);
  }
  discard() {
    localStorage.removeItem(this.key);
    sessionStorage.removeItem(this.pointer);
    this.parents = [];
  }
  complete() {
    for (const parent of this.parents) {
      const current = readSessionDraft(parent.key);
      if (current && (current.revision ?? "") === parent.revision)
        localStorage.removeItem(parent.key);
    }
    this.discard();
  }
}
