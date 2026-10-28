<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import {
  newOperationID,
  api,
  APIError,
  message,
  type Actor,
  type Project,
} from "../../api";
import type { Branch } from "./types";

interface TextPatch {
  id: string;
  group_id: string;
  resource_id: string;
  base_revision: string;
  base_hash: string;
  start_rune: number;
  end_rune: number;
  expected_slice_hash: string;
  replacement: string;
  reason: string;
}
interface PatchResult {
  target: { resource_id: string; revision: string; hash: string; text: string };
  result: { text: string; hash: string; applied: TextPatch[] };
}
interface PatchGroup {
  id: string;
  members: string[];
  patches: TextPatch[];
  compensates_release_id?: string;
}
interface DraftContent {
  base_ip_head: string;
  operation_id: string;
  group: PatchGroup;
  selected_resource_id: string;
  snippet: string;
  replacement: string;
  reason: string;
  compensation_source_id: string;
  compensation_reason: string;
}
interface SavedDraft {
  revision: number;
  content: DraftContent | null;
  stale: boolean;
}
const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: Branch;
  resources: { id: string; name: string; head: string }[];
  releases: {
    id: string;
    status: string;
    manifest: { artifacts: { resource_id: string }[] };
  }[];
  blocked: boolean;
}>();
const emit = defineEmits<{
  prepared: [string];
  blocking: [boolean];
  expired: [];
}>();
const identity = () => crypto.randomUUID().replaceAll("-", "");
const groupID = ref(identity());
const operation = ref<string>(newOperationID());
const baseIPHead = ref(props.branch.head);
const selected = ref("");
const snippet = ref("");
const replacement = ref("");
const reason = ref("");
const patches = ref<TextPatch[]>([]);
const preview = ref<PatchResult[]>([]);
const undoRelease = ref("");
const undoReason = ref("");
const compensates = ref("");
const conflicts = ref<{ resource_id: string; reason: string }[]>([]);
const busy = ref(false);
const error = ref("");
const notice = ref("");
const loading = ref(true);
const savingDraft = ref(false);
const draftError = ref("");
const draftConflict = ref(false);
const restoredStale = ref(false);
const savedJSON = ref("");
let draftRevision = 0;
let autosaveTimer: ReturnType<typeof setTimeout> | undefined;
let pendingSave: Promise<void> | undefined;
let epoch = 0;
let disposed = false;
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const unavailable = computed(
  () => busy.value || loading.value || props.blocked || !canEdit.value,
);
const formDirty = computed(
  () =>
    !!(snippet.value || replacement.value || reason.value || undoReason.value),
);
const dirty = computed(() => patches.value.length > 0 || formDirty.value);
const choices = computed(() => props.resources.filter((r) => r.head));
const undoChoices = computed(() =>
  props.releases.filter((r) => r.status === "PUBLISHED"),
);
const root = computed(() => `/projects/${props.project.id}/patches`);
const live = (ticket: number) => !disposed && ticket === epoch;
function title(id: string) {
  return props.resources.find((r) => r.id === id)?.name ?? "历史资料";
}
function input() {
  return {
    branch_id: props.branch.id,
    expected_ip_head: baseIPHead.value,
    operation_id: operation.value,
    group: {
      id: groupID.value,
      members: [...new Set(patches.value.map((p) => p.resource_id))],
      patches: patches.value,
      compensates_release_id: compensates.value || undefined,
    },
  };
}
const draftJSON = computed(() =>
  JSON.stringify({
    base_ip_head: baseIPHead.value,
    operation_id: operation.value,
    group: input().group,
    selected_resource_id: selected.value,
    snippet: snippet.value,
    replacement: replacement.value,
    reason: reason.value,
    compensation_source_id: undoRelease.value,
    compensation_reason: undoReason.value,
  } satisfies DraftContent),
);
const unsaved = computed(
  () => !loading.value && draftJSON.value !== savedJSON.value,
);
const draftPath = computed(
  () => `/projects/${props.project.id}/branches/${props.branch.id}/patch-draft`,
);
const draftStatus = computed(() =>
  loading.value
    ? "正在恢复修订草稿…"
    : draftError.value
      ? "修订草稿暂存失败，本页内容仍保留"
      : savingDraft.value || unsaved.value
        ? "正在自动暂存修订草稿…"
        : "修订草稿已自动暂存",
);

async function loadDraft() {
  const ticket = epoch;
  loading.value = true;
  clearTimeout(autosaveTimer);
  try {
    const saved = await api<SavedDraft>(draftPath.value);
    if (!live(ticket)) return;
    const d = saved.content;
    if (d) {
      baseIPHead.value = d.base_ip_head;
      operation.value = d.operation_id;
      groupID.value = d.group.id;
      patches.value = d.group.patches ?? [];
      compensates.value = d.group.compensates_release_id ?? "";
      selected.value = d.selected_resource_id;
      snippet.value = d.snippet;
      replacement.value = d.replacement;
      reason.value = d.reason;
      undoRelease.value = d.compensation_source_id;
      undoReason.value = d.compensation_reason;
    } else {
      resetDraft();
    }
    draftRevision = saved.revision;
    savedJSON.value = draftJSON.value;
    draftError.value = "";
    draftConflict.value = false;
    restoredStale.value = saved.stale;
    preview.value = [];
    conflicts.value = [];
  } catch (e) {
    if (live(ticket)) {
      draftError.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
      return; // Never overwrite an unread server draft with an empty form.
    }
  }
  if (live(ticket)) loading.value = false;
}

async function persistDraft(): Promise<void> {
  clearTimeout(autosaveTimer);
  if (pendingSave) return pendingSave;
  if (loading.value || !canEdit.value || !unsaved.value) return;
  if (draftConflict.value)
    throw new Error("另一页面已更新此草稿，请先处理版本冲突。");
  const ticket = epoch;
  const path = draftPath.value;
  savingDraft.value = true;
  draftError.value = "";
  const saveLoop = async () => {
    try {
      while (live(ticket) && unsaved.value) {
        const snapshot = draftJSON.value;
        const saved = await api<SavedDraft>(path, "PUT", {
          revision: draftRevision,
          content: JSON.parse(snapshot),
        });
        if (!live(ticket)) return;
        draftRevision = saved.revision;
        // A late reply acknowledges only its own snapshot, never new keystrokes.
        savedJSON.value = snapshot;
        restoredStale.value = saved.stale;
      }
    } catch (e) {
      if (live(ticket)) {
        draftError.value = message(e);
        draftConflict.value = e instanceof APIError && e.status === 409;
        if (e instanceof APIError && e.status === 401) emit("expired");
      }
      throw e;
    } finally {
      if (live(ticket)) {
        savingDraft.value = false;
        pendingSave = undefined;
      }
    }
  };
  pendingSave = saveLoop();
  return pendingSave;
}
function retryDraft() {
  void persistDraft().catch(() => {});
}
watch(
  draftJSON,
  () => {
    if (loading.value || disposed || !canEdit.value) return;
    clearTimeout(autosaveTimer);
    if (!draftError.value) autosaveTimer = setTimeout(retryDraft, 350);
  },
  { flush: "sync" },
);
async function hash(text: string) {
  const bytes = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(text),
  );
  return Array.from(new Uint8Array(bytes), (b) =>
    b.toString(16).padStart(2, "0"),
  ).join("");
}
async function run(work: (ticket: number) => Promise<void>) {
  if (unavailable.value) return;
  const ticket = epoch;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work(ticket);
  } catch (e) {
    if (live(ticket)) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (live(ticket)) busy.value = false;
  }
}
async function add() {
  if (compensates.value) return;
  await run(async (ticket) => {
    const ipHead = props.branch.head;
    if (!selected.value || !snippet.value || !reason.value.trim())
      throw new Error("请选择资料，并填写原文片段和修订理由。");
    const resource = await api<{
      id: string;
      head: string;
      hash: string;
      text: string;
    }>(`/projects/${props.project.id}/resources/${selected.value}`);
    if (!live(ticket)) return;
    const start = resource.text.indexOf(snippet.value);
    if (start < 0) throw new Error("当前正式稿中找不到这个片段，请核对原文。");
    if (resource.text.indexOf(snippet.value, start + 1) >= 0)
      throw new Error("此片段出现多次，请填写更长的原文以准确定位。");
    if (
      patches.value.some(
        (p) =>
          p.resource_id === resource.id && p.base_revision !== resource.head,
      )
    )
      throw new Error("资料已有新版本，请移除这份资料的旧修订后重新核对。");
    const anchorHash = await hash(snippet.value);
    if (!live(ticket)) return;
    const first = Array.from(resource.text.slice(0, start)).length;
    if (!patches.value.length) baseIPHead.value = ipHead;
    patches.value.push({
      id: identity(),
      group_id: groupID.value,
      resource_id: resource.id,
      base_revision: resource.head,
      base_hash: resource.hash,
      start_rune: first,
      end_rune: first + Array.from(snippet.value).length,
      expected_slice_hash: anchorHash,
      replacement: replacement.value,
      reason: reason.value,
    });
    snippet.value = "";
    replacement.value = "";
    reason.value = "";
    preview.value = [];
    operation.value = newOperationID();
  });
}
function remove(id: string) {
  if (unavailable.value || compensates.value) return;
  patches.value = patches.value.filter((p) => p.id !== id);
  preview.value = [];
  operation.value = newOperationID();
}
function resetDraft() {
  patches.value = [];
  preview.value = [];
  conflicts.value = [];
  compensates.value = "";
  undoReason.value = "";
  snippet.value = "";
  replacement.value = "";
  reason.value = "";
  selected.value = "";
  undoRelease.value = "";
  baseIPHead.value = props.branch.head;
  groupID.value = identity();
  operation.value = newOperationID();
}
async function discard() {
  await run(async () => {
    await persistDraft();
    resetDraft();
    await persistDraft();
  });
}
async function compensate() {
  if (
    patches.value.length ||
    snippet.value ||
    replacement.value ||
    reason.value
  )
    return;
  await run(async (ticket) => {
    const head = props.branch.head;
    const result = await api<{
      group: PatchGroup | null;
      items: PatchResult[];
      conflicts: typeof conflicts.value;
    }>(`/projects/${props.project.id}/compensations/preview`, "POST", {
      branch_id: props.branch.id,
      expected_ip_head: head,
      release_id: undoRelease.value,
      reason: undoReason.value,
    });
    if (!live(ticket)) return;
    conflicts.value = result.conflicts;
    if (!result.group) return;
    baseIPHead.value = head;
    groupID.value = result.group.id;
    patches.value = result.group.patches;
    compensates.value = result.group.compensates_release_id ?? "";
    preview.value = result.items;
    undoReason.value = "";
    operation.value = newOperationID();
  });
}
async function inspect() {
  await run(async (ticket) => {
    const result = await api<{ items: PatchResult[] }>(
      `${root.value}/preview`,
      "POST",
      input(),
    );
    if (live(ticket)) preview.value = result.items;
  });
}
async function save() {
  if (!preview.value.length || formDirty.value) return;
  await run(async (ticket) => {
    await persistDraft();
    if (!live(ticket)) return;
    const saved = await api<{ id: string }>(
      `${root.value}/releases`,
      "POST",
      input(),
    );
    if (!live(ticket)) return;
    resetDraft();
    notice.value =
      "修订候选已保存。请在联合候选中完成独立审查，再确认采用和发布。";
    emit("prepared", saved.id);
    await persistDraft();
  });
}
watch(
  [busy, loading, savingDraft, unsaved],
  () =>
    emit(
      "blocking",
      busy.value || loading.value || savingDraft.value || unsaved.value,
    ),
  {
    immediate: true,
  },
);
watch(
  () => `${props.project.id}:${props.branch.id}`,
  () => {
    epoch++;
    pendingSave = undefined;
    savingDraft.value = false;
    patches.value = [];
    preview.value = [];
    selected.value = "";
    snippet.value = "";
    replacement.value = "";
    reason.value = "";
    undoRelease.value = "";
    undoReason.value = "";
    compensates.value = "";
    conflicts.value = [];
    busy.value = false;
    error.value = "";
    notice.value = "";
    groupID.value = identity();
    operation.value = newOperationID();
    void loadDraft();
  },
);
function guard(event: BeforeUnloadEvent) {
  if (unsaved.value || busy.value || savingDraft.value) {
    event.preventDefault();
    event.returnValue = "";
  }
}
onMounted(() => {
  window.addEventListener("beforeunload", guard);
  void loadDraft();
});
onUnmounted(() => {
  disposed = true;
  epoch++;
  clearTimeout(autosaveTimer);
  window.removeEventListener("beforeunload", guard);
  emit("blocking", false);
});
</script>

<template>
  <details class="patch-panel">
    <summary>局部修订</summary>
    <p class="muted">
      选择正式稿中的具体片段，核对修改前后内容后保存为整组候选。原稿和正在编辑的草稿都会保留。
    </p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status" class="notice">{{ notice }}</p>
    <p data-testid="patch-draft-status" role="status">{{ draftStatus }}</p>
    <p v-if="restoredStale" class="notice">
      正式稿或设定已更新。已恢复原有修订锚点，请重新核对；系统不会自动覆盖新稿。
    </p>
    <div v-if="draftError" role="alert" class="error">
      <p>{{ draftError }}</p>
      <p v-if="draftConflict">
        另一页面已保存新版本，本页编辑仍在。可先复制本页内容，再选择读取最新草稿。
      </p>
      <button
        v-if="!draftConflict && !loading"
        :disabled="savingDraft"
        @click="retryDraft"
      >
        重试暂存
      </button>
      <button :disabled="busy || savingDraft" @click="loadDraft">
        {{ loading ? "重试恢复草稿" : "放弃本页编辑，读取最新草稿" }}
      </button>
    </div>
    <details v-if="canEdit">
      <summary>撤回已发布的正文改动</summary>
      <p class="muted">
        生成基于当前稿的新候选，保留后续编辑。此操作只撤回所选版本的正文改动；设定、资料用途和依赖须另行核对。范围重叠时先处理冲突，再完整保存。
      </p>
      <form @submit.prevent="compensate">
        <fieldset
          :disabled="
            unavailable ||
            patches.length > 0 ||
            !!snippet ||
            !!replacement ||
            !!reason
          "
        >
          <label :for="`compensation-release-${branch.id}`"
            >要撤回正文改动的版本</label
          >
          <select
            :id="`compensation-release-${branch.id}`"
            v-model="undoRelease"
            required
          >
            <option value="">请选择已发布版本</option>
            <option
              v-for="release in undoChoices"
              :key="release.id"
              :value="release.id"
            >
              {{
                release.manifest.artifacts
                  .map((r) => title(r.resource_id))
                  .join("、")
              }}
              · {{ release.id.slice(0, 8) }}
            </option>
          </select>
          <label
            >撤回正文理由<textarea
              v-model="undoReason"
              required
              maxlength="2000"
            />
          </label>
          <button>计算保留后续编辑的撤回方案</button>
        </fieldset>
      </form>
      <ul v-if="conflicts.length" role="alert" class="error">
        <li v-for="conflict in conflicts" :key="conflict.resource_id">
          {{ title(conflict.resource_id) }}：{{ conflict.reason }}
        </li>
      </ul>
    </details>
    <p v-if="compensates" class="notice">
      已保留后续编辑。整组撤回候选仍须独立审查、确认采用和发布；原版本继续保留。
    </p>
    <form v-if="canEdit" @submit.prevent="add">
      <fieldset :disabled="unavailable || !!compensates || !!undoReason">
        <label :for="`patch-resource-${branch.id}`">修订目标资料</label>
        <select :id="`patch-resource-${branch.id}`" v-model="selected" required>
          <option value="">请选择已发布资料</option>
          <option
            v-for="resource in choices"
            :key="resource.id"
            :value="resource.id"
          >
            {{ resource.name }}
          </option>
        </select>
        <label
          >原文片段<textarea
            v-model="snippet"
            required
            rows="3"
            maxlength="262144"
          />
        </label>
        <label
          >替换内容<textarea
            v-model="replacement"
            rows="3"
            maxlength="262144"
          />
        </label>
        <p class="muted">替换内容留空表示删除所选片段。</p>
        <label
          >局部修订理由<textarea
            v-model="reason"
            required
            rows="2"
            maxlength="2000"
          />
        </label>
        <button>加入修订组</button>
      </fieldset>
    </form>
    <ul v-if="patches.length">
      <li v-for="patch in patches" :key="patch.id">
        <strong>{{ title(patch.resource_id) }}</strong> · {{ patch.reason }}
        <button
          type="button"
          :disabled="unavailable || !!compensates"
          :aria-label="`移除修订 ${title(patch.resource_id)} ${patches.indexOf(patch) + 1}`"
          @click="remove(patch.id)"
        >
          移除此项
        </button>
      </li>
    </ul>
    <button
      v-if="dirty || conflicts.length"
      class="quiet"
      :disabled="unavailable"
      @click="discard"
    >
      放弃当前修订组
    </button>
    <button
      v-if="patches.length"
      type="button"
      :disabled="unavailable || formDirty"
      @click="inspect"
    >
      核对整组修订
    </button>
    <div v-if="preview.length" data-testid="patch-group-preview">
      <article v-for="item in preview" :key="item.target.resource_id">
        <h3>{{ title(item.target.resource_id) }}</h3>
        <div class="patch-comparison">
          <section>
            <h4>修改前</h4>
            <pre>{{ item.target.text }}</pre>
          </section>
          <section>
            <h4>修改后</h4>
            <pre>{{ item.result.text }}</pre>
          </section>
        </div>
      </article>
      <button type="button" :disabled="unavailable || formDirty" @click="save">
        保存修订候选
      </button>
    </div>
  </details>
</template>

<style scoped>
.patch-panel {
  margin-block: 1rem;
}
fieldset {
  border: 0;
  padding: 0;
}
.patch-comparison {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
@media (max-width: 700px) {
  .patch-comparison {
    grid-template-columns: 1fr;
  }
}
</style>
