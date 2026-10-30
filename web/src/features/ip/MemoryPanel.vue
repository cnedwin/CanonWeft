<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";
import type { Branch } from "./types";
import MemoryEditor from "./MemoryEditor.vue";
import MemorySessionEditor from "./MemorySessionEditor.vue";
import MemoryPackView from "./MemoryPackView.vue";
import MemoryExtractionPanel from "./MemoryExtractionPanel.vue";
import {
  SessionDraftStore,
  type MemorySessionWrite,
  type RecoverableSessionDraft,
} from "./sessionDraft";
import {
  copyMemory,
  memoryKinds,
  summaryLevels,
  type SummaryPin,
  type MemoryRefreshTask,
  memoryStates,
  type MemoryItem,
  type MemoryPack,
  type MemoryPurpose,
  type MemoryResource,
  type MemoryResourceDetail,
  type MemorySession,
  type MemorySessionDocument,
  type MemorySource,
} from "./memory";

const props = withDefaults(
  defineProps<{
    project: Project;
    actor: Actor;
    branch: Branch;
    blocked: boolean;
    publicationRevision?: number;
  }>(),
  { publicationRevision: 0 },
);
const emit = defineEmits<{
  expired: [];
  blocking: [boolean];
  attention: [count: number];
}>();
const activeSection = ref<"items" | "extraction" | "sessions" | "materials">(
  "items",
);
const sections = [
  { id: "items", label: "记忆浏览" },
  { id: "extraction", label: "整理候选" },
  { id: "sessions", label: "短期会话" },
  { id: "materials", label: "写作材料" },
] as const;
const extractionPanel = ref<InstanceType<typeof MemoryExtractionPanel> | null>(
  null,
);
const extractionAttention = ref(0);
function updateExtractionAttention(count: number) {
  extractionAttention.value = count;
  emit("attention", count);
}
async function focusExtractionTasks() {
  activeSection.value = "extraction";
  await nextTick();
  await extractionPanel.value?.focusTasks();
}
defineExpose({ focusExtractionTasks });
const root = computed(
  () => `/projects/${props.project.id}/branches/${props.branch.id}/memory`,
);
const items = ref<MemoryItem[]>([]);
const refreshTasks = ref<MemoryRefreshTask[]>([]);
const sessions = ref<MemorySession[]>([]);
const expiredMemoryCount = ref(0);
const resources = ref<MemoryResource[]>([]);
const busy = ref(false);
const sourceBusy = ref(false);
const extractionBusy = ref(false);
const editorDirty = ref(false);
const sessionDirty = ref(false);
const error = ref("");
const notice = ref("");
const sessionID = ref("");
const sessionBase = ref<MemorySession | null>(null);
const sessionDraft = ref<MemorySessionDocument | null>(null);
const pendingSession = ref<MemorySessionWrite | null>(null);
const sessionConflict = ref(false);
const latestSession = ref<MemorySession | null>(null);
const sessionExpired = computed(
  () =>
    sessionBase.value?.retention?.state === "EXPIRED" ||
    sessions.value.some(
      (session) =>
        session.id === sessionBase.value?.id &&
        session.retention?.state === "EXPIRED",
    ),
);
const draftWarning = ref("");
const sessionStorageKey = computed(
  () =>
    `memory-session-draft:${props.actor.id}:${props.project.id}:${props.branch.id}`,
);
const sessionDraftStore = new SessionDraftStore(sessionStorageKey.value);
const recoverableSessionDrafts = ref<RecoverableSessionDraft[]>([]);
const showSessionEditor = ref(false);
const sessionEditorKey = ref(0);
const editing = ref<MemoryItem | null>(null);
const showEditor = ref(false);
const editorKey = ref(0);
const kindFilter = ref("");
const stateFilter = ref("");
const browseQuery = ref("");
const confirmation = ref<{
  action: "adopt" | "revoke";
  item: MemoryItem;
} | null>(null);
const query = ref("");
const purpose = ref<MemoryPurpose>("author");
const perspective = ref("");
const storyTime = ref(0);
const narrativeTime = ref(0);
const requiredIDs = ref<string[]>([]);
const pack = ref<MemoryPack | null>(null);
const packSignature = ref("");
const packInvalidated = ref(false);
const sourceExcerpt = ref<{
  title: string;
  text: string;
  item?: MemoryItem;
} | null>(null);
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const canAdopt = canEdit;
const dirty = computed(
  () => editorDirty.value || sessionDirty.value || !!pendingSession.value,
);
const locked = computed(
  () => busy.value || sourceBusy.value || extractionBusy.value || props.blocked,
);
const visibleItems = computed(() =>
  items.value.filter(
    (item) =>
      (!kindFilter.value || item.kind === kindFilter.value) &&
      (!stateFilter.value || item.state === stateFilter.value) &&
      `${item.text} ${item.summary?.title ?? ""}`
        .normalize("NFKC")
        .toLocaleLowerCase()
        .includes(
          browseQuery.value.normalize("NFKC").trim().toLocaleLowerCase(),
        ),
  ),
);
const compileInput = computed(() => ({
  purpose: purpose.value,
  perspective: perspective.value,
  story_time: storyTime.value,
  narrative_time: narrativeTime.value,
  session: sessionID.value,
  timeline: "main",
  required_ids: [...requiredIDs.value],
  query: query.value,
  budget: 65536,
}));
const currentSignature = computed(() =>
  JSON.stringify([
    compileInput.value,
    props.branch.head,
    props.publicationRevision,
  ]),
);
const stalePack = computed(
  () => packInvalidated.value || packSignature.value !== currentSignature.value,
);
let disposed = false;
let loadSequence = 0;
let saveOperation = { signature: "", id: "" };
const actionOperations = new Map<string, string>();
const scope = () => `${props.actor.id}/${props.project.id}/${props.branch.id}`;
const live = (key: string) => !disposed && key === scope();
watch(
  [dirty, busy, sourceBusy, extractionBusy],
  () =>
    emit(
      "blocking",
      dirty.value || busy.value || sourceBusy.value || extractionBusy.value,
    ),
  { immediate: true },
);
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function loadSnapshot() {
  const ticket = ++loadSequence;
  const key = scope();
  const [data, sourceList] = await Promise.all([
    api<{
      items: MemoryItem[];
      sessions: MemorySession[];
      expired?: { id: string }[];
      refresh_tasks?: MemoryRefreshTask[];
    }>(root.value),
    api<{ items: MemoryResource[] }>(
      `/projects/${props.project.id}/resources?branch=${props.branch.id}`,
    ),
  ]);
  if (!live(key) || ticket !== loadSequence) return;
  items.value = data.items ?? [];
  refreshTasks.value = data.refresh_tasks ?? [];
  sessions.value = data.sessions ?? [];
  expiredMemoryCount.value = data.expired?.length ?? 0;
  resources.value = sourceList.items.filter((resource) => resource.head);
}
async function run(work: (key: string) => Promise<void>) {
  if (locked.value) return;
  const key = scope();
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work(key);
  } catch (e) {
    if (live(key)) fail(e);
  } finally {
    if (live(key)) busy.value = false;
  }
}
async function refresh() {
  await run(async () => {
    await loadSnapshot();
    packInvalidated.value = true;
  });
}
function chooseSession() {
  sessionBase.value = copyMemory(
    sessions.value.find((session) => session.id === sessionID.value) ?? null,
  );
  showSessionEditor.value = !!sessionBase.value && canEdit.value;
  sessionDirty.value = false;
  sessionEditorKey.value++;
  sessionDraft.value = null;
  sessionConflict.value = false;
  latestSession.value = null;
  persistSessionDraft();
  requiredIDs.value = [...(sessionBase.value?.document.focus_ids ?? [])];
}
function editCurrentSession() {
  if (locked.value || !canEdit.value || !sessionID.value) return;
  if (!showSessionEditor.value) {
    if (dirty.value) return;
    chooseSession();
  }
  activeSection.value = "sessions";
}
function newSession() {
  if (locked.value || dirty.value) return;
  activeSection.value = "sessions";
  sessionID.value = "";
  sessionBase.value = null;
  sessionDraft.value = null;
  sessionConflict.value = false;
  latestSession.value = null;
  persistSessionDraft();
  showSessionEditor.value = true;
  sessionEditorKey.value++;
}
function discardSession() {
  if (pendingSession.value) return;
  sessionDraft.value = null;
  sessionConflict.value = false;
  latestSession.value = null;
  persistSessionDraft();
  sessionDirty.value = false;
  showSessionEditor.value = false;
  sessionEditorKey.value++;
}
async function saveSession(document: MemorySessionDocument) {
  if (
    !canEdit.value ||
    pendingSession.value ||
    locked.value ||
    sessionExpired.value
  )
    return;
  sessionDraft.value = copyMemory(document);
  pendingSession.value = {
    id: sessionBase.value?.id ?? "",
    expected_revision: sessionBase.value?.revision ?? 0,
    document: copyMemory(document),
    operation_id: newOperationID(),
  };
  await retrySession();
}
function persistSessionDraft() {
  try {
    const document = sessionDraft.value;
    const unchanged =
      JSON.stringify(document) === JSON.stringify(sessionBase.value?.document);
    sessionDraftStore.write(
      !document || (!pendingSession.value && unchanged)
        ? null
        : {
            schema: 1,
            base: copyMemory(sessionBase.value),
            document: copyMemory(document),
            pending: copyMemory(pendingSession.value),
            conflict: sessionConflict.value,
          },
    );
    draftWarning.value = "";
    recoverableSessionDrafts.value = sessionDraftStore.list();
    return true;
  } catch {
    draftWarning.value =
      "本机暂存未能写入；本次新请求尚未发送。请保留笔记，恢复存储空间或权限后重试。";
    return false;
  }
}
function changeSession(document: MemorySessionDocument) {
  if (pendingSession.value) return;
  sessionDraft.value = copyMemory(document);
  persistSessionDraft();
}
function restoreSessionDraft(selected?: string) {
  try {
    recoverableSessionDrafts.value = sessionDraftStore.list();
    const draft = sessionDraftStore.read(selected);
    if (!draft) return;
    sessionBase.value = draft.base;
    sessionID.value = draft.base?.id ?? "";
    sessionDraft.value = draft.document;
    pendingSession.value = draft.pending;
    sessionConflict.value = draft.conflict;
    showSessionEditor.value = true;
    activeSection.value = "sessions";
    sessionEditorKey.value++;
    notice.value = "已恢复本机工作记录；请核对后保存，不会自动采用任何内容。";
  } catch (e) {
    draftWarning.value = message(e);
  }
}
async function retrySession() {
  if (!canEdit.value || !pendingSession.value || sessionExpired.value) return;
  if (!persistSessionDraft()) return;
  const request = copyMemory(pendingSession.value);
  await run(async (key) => {
    let saved: MemorySession;
    try {
      saved = await api<MemorySession>(
        `${root.value}/sessions`,
        "POST",
        request,
      );
    } catch (e) {
      if (
        live(key) &&
        e instanceof APIError &&
        [400, 409, 422].includes(e.status)
      ) {
        pendingSession.value = null;
        sessionConflict.value = e.status === 409 && !!sessionBase.value;
        persistSessionDraft();
      }
      throw e;
    }
    if (!live(key)) return;
    pendingSession.value = null;
    sessionID.value = saved.id;
    sessionBase.value = copyMemory(saved);
    sessionDraft.value = null;
    sessionConflict.value = false;
    latestSession.value = null;
    try {
      sessionDraftStore.complete();
      recoverableSessionDrafts.value = sessionDraftStore.list();
      draftWarning.value = "";
    } catch {
      draftWarning.value =
        "服务器已保存，但本机旧暂存清理未完成；不要将旧草稿当作新会话重复创建。";
    }
    sessionDirty.value = false;
    sessionEditorKey.value++;
    packInvalidated.value = true;
    notice.value = "工作记录已保存，可以继续整理记忆候选。";
    await loadSnapshot();
  });
}
function copyExpiredSessionDraft() {
  if (locked.value || !canEdit.value || !sessionExpired.value) return;
  const document = copyMemory(
    sessionDraft.value ?? sessionBase.value?.document,
  );
  if (!document) return;
  sessionID.value = "";
  sessionBase.value = null;
  sessionDraft.value = document;
  pendingSession.value = null;
  sessionConflict.value = false;
  latestSession.value = null;
  sessionEditorKey.value++;
  persistSessionDraft();
  notice.value = "本机输入已转为新会话草稿，请确认后保存。";
}
async function readLatestSession() {
  if (!sessionBase.value || pendingSession.value) return;
  const id = sessionBase.value.id;
  await run(async (key) => {
    const latest = await api<MemorySession>(`${root.value}/sessions/${id}`);
    if (live(key) && id === sessionBase.value?.id) latestSession.value = latest;
  });
}
function useLatestSession() {
  if (locked.value || pendingSession.value || !latestSession.value) return;
  sessionBase.value = copyMemory(latestSession.value);
  sessionConflict.value = false;
  latestSession.value = null;
  sessionEditorKey.value++;
  persistSessionDraft();
  notice.value =
    "已保留你的输入并切换到最新基线；请对照修改，明确保存后才更新服务器记录。";
}
function edit(item: MemoryItem | null) {
  if (locked.value || dirty.value) return;
  activeSection.value = "items";
  editing.value = copyMemory(item);
  showEditor.value = true;
  editorKey.value++;
  confirmation.value = null;
}
function discardEditor() {
  editorDirty.value = false;
  showEditor.value = false;
  editing.value = null;
  editorKey.value++;
}
async function saveItem(item: MemoryItem) {
  if (!canEdit.value) return;
  const body = { item, expected_revision: editing.value?.revision ?? "" };
  const signature = JSON.stringify(body);
  if (saveOperation.signature !== signature)
    saveOperation = { signature, id: newOperationID() };
  await run(async (key) => {
    const saved = await api<MemoryItem>(`${root.value}/items`, "POST", {
      ...body,
      operation_id: saveOperation.id,
    });
    if (!live(key)) return;
    editing.value = copyMemory(saved);
    editorDirty.value = false;
    editorKey.value++;
    packInvalidated.value = true;
    saveOperation = { signature: "", id: "" };
    notice.value = "记忆候选已保存，尚未确认采用。请在列表中检查后明确采用。";
    await loadSnapshot();
  });
}
function requestAction(action: "adopt" | "revoke", item: MemoryItem) {
  if (locked.value || dirty.value || !canAdopt.value) return;
  confirmation.value = { action, item: copyMemory(item) };
}
async function confirmAction() {
  const pending = confirmation.value;
  if (!pending) return;
  const operationKey = `${pending.action}/${pending.item.id}/${pending.item.revision}`;
  if (!actionOperations.has(operationKey))
    actionOperations.set(operationKey, newOperationID());
  await run(async (key) => {
    await api<MemoryItem>(
      `${root.value}/items/${pending.item.id}/${pending.action}`,
      "POST",
      {
        expected_revision: pending.item.revision,
        operation_id: actionOperations.get(operationKey),
      },
    );
    if (!live(key)) return;
    confirmation.value = null;
    if (editing.value?.id === pending.item.id) discardEditor();
    packInvalidated.value = true;
    notice.value =
      pending.action === "adopt"
        ? "已明确采用这条记忆；它仍不替代故事正史的正式发布。"
        : "已撤回这条记忆，历史修订仍保留。";
    await loadSnapshot();
  });
}
async function compile() {
  if (purpose.value === "writer" && !perspective.value) {
    error.value = "正文写作需要选择当前视角。";
    return;
  }
  const signature = currentSignature.value;
  await run(async (key) => {
    const result = await api<MemoryPack>(
      `${root.value}/compile`,
      "POST",
      copyMemory(compileInput.value),
    );
    if (!live(key)) return;
    if (signature !== currentSignature.value) {
      notice.value = "使用范围已变化，请重新编译。";
      return;
    }
    pack.value = result;
    packSignature.value = signature;
    packInvalidated.value = false;
    sourceExcerpt.value = null;
  });
}
async function viewSource(source: MemorySource) {
  await run(async (key) => {
    sourceExcerpt.value = null;
    const resource = await api<MemoryResourceDetail>(
      `/projects/${props.project.id}/resources/${source.resource}`,
    );
    if (!live(key)) return;
    let text = resource.text;
    let historical = false;
    if (resource.head !== source.revision || resource.hash !== source.hash) {
      const release = await api<{
        id: string;
        status: string;
        manifest: {
          branch_id: string;
          artifacts: { resource_id: string; hash: string }[];
        };
        contents: { resource_id: string; text: string }[];
      }>(`/projects/${props.project.id}/releases/${source.revision}`);
      if (!live(key)) return;
      const frozen = release.contents.find(
        (value) => value.resource_id === source.resource,
      );
      if (
        release.id !== source.revision ||
        release.status !== "PUBLISHED" ||
        release.manifest.branch_id !== props.branch.id ||
        !release.manifest.artifacts.some(
          (value) =>
            value.resource_id === source.resource && value.hash === source.hash,
        ) ||
        !frozen
      )
        throw new Error("历史来源未通过核对。");
      const hash = Array.from(
        new Uint8Array(
          await crypto.subtle.digest(
            "SHA-256",
            new TextEncoder().encode(frozen.text),
          ),
        ),
      )
        .map((byte) => byte.toString(16).padStart(2, "0"))
        .join("");
      if (!live(key)) return;
      if (hash !== source.hash) throw new Error("历史原文内容未通过核对。");
      text = frozen.text;
      historical = true;
    }
    const characters = Array.from(text);
    if (
      source.start_rune < 0 ||
      source.end_rune > characters.length ||
      source.end_rune <= source.start_rune
    )
      throw new Error("来源位置未通过核对，未显示不一致原文。");
    sourceExcerpt.value = {
      title: `${resource.name}${historical ? " · 引用时的历史原文" : ""} · 第 ${source.start_rune + 1}—${source.end_rune} 字`,
      text: characters.slice(source.start_rune, source.end_rune).join(""),
    };
  });
}
async function viewSummary(pin: SummaryPin) {
  await run(async (key) => {
    sourceExcerpt.value = null;
    const item = await api<MemoryItem>(
      `${root.value}/items/${pin.id}/revisions/${pin.revision}`,
    );
    const hash = Array.from(
      new Uint8Array(
        await crypto.subtle.digest(
          "SHA-256",
          new TextEncoder().encode(item.text),
        ),
      ),
    )
      .map((byte) => byte.toString(16).padStart(2, "0"))
      .join("");
    if (!live(key)) return;
    if (
      item.id !== pin.id ||
      item.revision !== pin.revision ||
      hash !== pin.hash
    )
      throw new Error("摘要来源版本未通过核对。");
    sourceExcerpt.value = {
      title: `${item.summary?.title ?? "摘要"} · 引用时的版本`,
      text: item.text,
      item,
    };
  });
}
function guardUnload(event: BeforeUnloadEvent) {
  if (dirty.value || busy.value || sourceBusy.value || extractionBusy.value) {
    event.preventDefault();
    event.returnValue = "";
  }
}
watch([() => props.branch.head, () => props.publicationRevision], () => {
  packInvalidated.value = true;
  sourceExcerpt.value = null;
  void loadSnapshot().catch((e) => {
    if (!disposed) fail(e);
  });
});
onMounted(() => {
  restoreSessionDraft();
  window.addEventListener("beforeunload", guardUnload);
  void refresh();
});
onBeforeUnmount(() => {
  disposed = true;
  loadSequence++;
  window.removeEventListener("beforeunload", guardUnload);
  emit("blocking", false);
  emit("attention", 0);
});
</script>

<template>
  <section class="panel memory-panel" data-testid="memory-panel">
    <header class="section-heading">
      <div>
        <p class="eyebrow">当前分支的创作记忆</p>
        <h2>创作记忆</h2>
      </div>
      <span class="pill">{{ items.length }} 条已读取记忆</span>
    </header>
    <p class="muted author-page-description">
      围绕主题、主线、高光、关系和摘要整理长记忆；私人会话保留当前目标与短期笔记。候选必须明确采用，记忆不替代故事正史。
    </p>
    <nav class="author-page-nav" aria-label="创作记忆分区">
      <button
        v-for="section in sections"
        :key="section.id"
        type="button"
        class="quiet"
        :aria-pressed="activeSection === section.id"
        @click="activeSection = section.id"
      >
        {{ section.label }}
      </button>
    </nav>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p v-if="draftWarning" class="error" role="alert">{{ draftWarning }}</p>
    <p v-if="dirty" class="notice">
      有尚未保存的记忆输入，请保存或放弃后再切换工作会话、分支或页面。
      <button
        v-if="editorDirty && activeSection !== 'items'"
        type="button"
        class="quiet"
        @click="activeSection = 'items'"
      >
        继续编辑记忆候选
      </button>
      <button
        v-if="(sessionDirty || pendingSession) && activeSection !== 'sessions'"
        type="button"
        class="quiet"
        @click="activeSection = 'sessions'"
      >
        查看工作记录输入
      </button>
    </p>
    <p
      v-if="pendingSession && activeSection !== 'sessions'"
      class="author-status"
    >
      上次工作记录的保存结果尚未确认，原请求与输入仍保留，请先核对。
    </p>
    <p v-if="confirmation && activeSection !== 'items'" class="author-status">
      有等待你明确确认的记忆变更。
      <button type="button" class="quiet" @click="activeSection = 'items'">
        查看记忆变更
      </button>
    </p>
    <p
      v-if="pack && stalePack && activeSection !== 'materials'"
      class="author-status"
    >
      上次写作材料的范围或来源已变化，仅供历史查看。
      <button type="button" class="quiet" @click="activeSection = 'materials'">
        查看历史材料
      </button>
    </p>
    <p v-if="extractionAttention" class="author-status" role="status">
      已读取的整理任务中有
      {{ extractionAttention }}
      项结果或费用需核对。不自动重发，已有费用占用保留。
      <button type="button" class="quiet" @click="focusExtractionTasks">
        查看记忆整理任务
      </button>
    </p>
    <div class="actions">
      <button type="button" class="quiet" :disabled="locked" @click="refresh">
        刷新记忆列表</button
      ><button
        v-if="canEdit"
        type="button"
        :disabled="locked || dirty"
        @click="newSession"
      >
        新建工作会话
      </button>
      <button
        v-if="canEdit && sessionID"
        type="button"
        class="quiet"
        @click="editCurrentSession"
      >
        编辑当前工作会话
      </button>
    </div>
    <label
      >当前工作会话<select
        aria-label="当前工作会话"
        v-model="sessionID"
        :disabled="locked || dirty"
        @change="chooseSession"
      >
        <option value="">请选择私人工作会话</option>
        <option
          v-for="session in sessions"
          :key="session.id"
          :value="session.id"
          :disabled="session.retention?.state === 'EXPIRED'"
        >
          {{
            session.retention?.state === "EXPIRED"
              ? "已过期的工作会话"
              : session.document.title || "未命名工作会话"
          }}
        </option>
      </select></label
    >
    <p v-if="expiredMemoryCount" class="muted" role="status">
      {{ expiredMemoryCount }}
      条过期记忆已清理，不再用于写作。已采用的长期记忆继续保留。
    </p>
    <div v-show="activeSection === 'sessions'" class="author-work-area">
      <p
        v-if="!showSessionEditor && !recoverableSessionDrafts.length"
        class="muted"
      >
        选择一个私人工作会话，或新建会话记录当前目标与笔记。
      </p>
      <template v-if="showSessionEditor && canEdit">
        <div v-if="sessionExpired" class="notice" role="status">
          <p>
            原工作会话已过期，不能继续编辑。已采用的长期记忆仍保留，本机未保存的输入也仍在。
          </p>
          <button
            type="button"
            :disabled="locked"
            @click="copyExpiredSessionDraft"
          >
            保留输入，转为新会话
          </button>
        </div>
        <div v-if="pendingSession && !sessionExpired" class="notice">
          <p>
            上次保存结果尚未确认。先用原请求核对，避免重复创建或覆盖后续记录。
          </p>
          <button type="button" :disabled="locked" @click="retrySession">
            核对并重试上次保存
          </button>
        </div>
        <div v-if="sessionConflict">
          <p>
            服务器记录已变化，你的输入仍然保留。读取最新内容并比较后，再选择编辑基线。
          </p>
          <button type="button" :disabled="locked" @click="readLatestSession">
            读取服务器最新记录
          </button>
          <div v-if="latestSession" data-testid="latest-session">
            <h3>服务器最新记录 · 第{{ latestSession.revision }}版</h3>
            <p>{{ latestSession.document.title }}</p>
            <pre
              >{{ latestSession.document.goal }}\n{{
                latestSession.document.notes
              }}</pre
            >
            <button type="button" :disabled="locked" @click="useLatestSession">
              保留我的输入，改用最新版本继续编辑
            </button>
          </div>
        </div>
        <MemorySessionEditor
          :key="sessionEditorKey"
          :session="sessionBase"
          :draft="sessionDraft"
          :items="items"
          :blocked="locked || !!pendingSession || sessionExpired"
          @save="saveSession"
          @change="changeSession"
          @dirty="sessionDirty = $event"
        />
        <button
          type="button"
          class="quiet"
          :disabled="locked || !!pendingSession"
          @click="discardSession"
        >
          关闭工作记录编辑（放弃未保存内容）
        </button>
      </template>
      <div
        v-if="!showSessionEditor && canEdit && recoverableSessionDrafts.length"
      >
        <h3>可恢复的本机工作记录</h3>
        <p>来自此前页面或其他窗口；打开后独立编辑，不覆盖原窗口的草稿。</p>
        <ul>
          <li v-for="draft in recoverableSessionDrafts" :key="draft.key">
            {{ draft.title }}{{ draft.pending ? "（上次保存待核对）" : "" }}
            <button
              type="button"
              :disabled="locked || dirty"
              @click="restoreSessionDraft(draft.key)"
            >
              恢复：{{ draft.title }}
            </button>
          </li>
        </ul>
      </div>
    </div>
    <div v-show="activeSection === 'extraction'" class="author-work-area">
      <MemoryExtractionPanel
        ref="extractionPanel"
        v-if="canEdit"
        :key="`${actor.id}/${project.id}/${branch.id}`"
        :actor="actor"
        :project="project"
        :branch="branch"
        :resources="resources"
        :items="items"
        :session-id="sessionID"
        :refresh-tasks="refreshTasks"
        :blocked="props.blocked || busy || sourceBusy || dirty"
        @blocking="extractionBusy = $event"
        @attention="updateExtractionAttention"
        @expired="emit('expired')"
        @imported="
          loadSnapshot().catch(fail);
          packInvalidated = true;
        "
      />
    </div>
    <div v-show="activeSection === 'items'" class="author-work-area">
      <div class="actions">
        <h3>记忆条目</h3>
        <button
          v-if="canEdit"
          type="button"
          :disabled="locked || dirty"
          @click="edit(null)"
        >
          新增记忆候选
        </button>
      </div>
      <div class="memory-browser-controls">
        <label
          >搜索记忆内容<input
            v-model="browseQuery"
            type="search"
            aria-label="搜索记忆内容"
            placeholder="记忆内容或摘要名称"
        /></label>
        <label
          >筛选记忆类别<select v-model="kindFilter" aria-label="筛选记忆类别">
            <option value="">全部长短期记忆</option>
            <option
              v-for="(name, kind) in memoryKinds"
              :key="kind"
              :value="kind"
            >
              {{ name }}
            </option>
          </select></label
        >
        <label
          >筛选记忆状态<select v-model="stateFilter" aria-label="筛选记忆状态">
            <option value="">全部状态</option>
            <option
              v-for="(name, state) in memoryStates"
              :key="state"
              :value="state"
            >
              {{ name }}
            </option>
          </select></label
        >
      </div>
      <p class="muted" role="status">
        当前筛选 {{ visibleItems.length }} / {{ items.length }} 条已读取记忆
      </p>
      <p v-if="!items.length" class="muted">
        暂无可显示的记忆；从已确认资料中引用原文，保存第一条候选。
      </p>
      <p v-else-if="!visibleItems.length" class="muted">
        没有匹配记忆，请调整搜索词、类别或状态。
      </p>
      <article
        v-for="item in visibleItems"
        :key="item.id"
        class="memory-card"
        :data-testid="`memory-item-${item.id}`"
      >
        <strong
          >{{ memoryKinds[item.kind] }} · {{ memoryStates[item.state] }}</strong
        >
        <p v-if="item.summary">
          {{ summaryLevels[item.summary.level] }}摘要 · {{ item.summary.title }}
        </p>
        <div v-if="item.summary?.inputs?.length">
          <button
            v-for="pin in item.summary.inputs"
            :key="pin.id"
            type="button"
            class="quiet"
            :disabled="locked"
            @click="viewSummary(pin)"
          >
            查看下层摘要
            {{
              items.find((child) => child.id === pin.id)?.summary?.title ??
              pin.id.slice(0, 8)
            }}
          </button>
        </div>
        <p v-if="item.state === 'stale'" class="notice">
          来源过期，请修订来源并重新确认。
        </p>
        <p v-if="['candidate', 'working'].includes(item.state)" class="muted">
          尚未确认，仅在相应私人工作范围使用。
        </p>
        <p class="memory-text">{{ item.text }}</p>
        <p class="muted">
          {{ item.control ? "隐藏创作约束" : "按可见范围提供证据" }} ·
          {{ item.pinned ? "适用范围内必须纳入" : "按相关性纳入" }} · 故事
          {{ item.from }} 起 · 揭示位置 {{ item.reveal_at }}
        </p>
        <ul>
          <li v-for="(source, index) in item.sources" :key="index">
            {{
              resources.find((r) => r.id === source.resource)?.name ??
              "来源资料不可用"
            }}
            · 第 {{ source.start_rune + 1 }}—{{ source.end_rune }} 字
            <span
              v-if="
                resources.find((r) => r.id === source.resource)?.head !==
                source.revision
              "
              >（来源版本需刷新）</span
            >
            <button
              type="button"
              class="quiet"
              :disabled="locked"
              @click="viewSource(source)"
            >
              查看来源
            </button>
          </li>
        </ul>
        <div class="actions">
          <button
            v-if="canEdit && item.state !== 'revoked'"
            type="button"
            class="quiet"
            :disabled="locked || dirty"
            @click="edit(item)"
          >
            编辑记忆
          </button>
          <button
            v-if="canAdopt && item.state === 'candidate'"
            type="button"
            :disabled="locked || dirty"
            @click="requestAction('adopt', item)"
          >
            检查并采用记忆
          </button>
          <button
            v-if="canAdopt && !['revoked', 'stale'].includes(item.state)"
            type="button"
            class="quiet"
            :disabled="locked || dirty"
            @click="requestAction('revoke', item)"
          >
            撤回记忆
          </button>
        </div>
      </article>
      <aside
        v-if="confirmation"
        class="memory-card"
        role="region"
        aria-label="确认记忆变更"
      >
        <h3>
          {{
            confirmation.action === "adopt"
              ? "确认采用此记忆"
              : "确认撤回此记忆"
          }}
        </h3>
        <p class="memory-text">{{ confirmation.item.text }}</p>
        <p>
          {{
            confirmation.action === "adopt"
              ? "采用后可在获准范围共享；来源事实与创作约束由服务端区分，均不替代正式正史。"
              : "撤回后不再用于新上下文，历史修订保留。"
          }}
        </p>
        <div class="actions">
          <button type="button" :disabled="locked" @click="confirmAction">
            {{
              confirmation.action === "adopt"
                ? "确认采用这条记忆"
                : "确认撤回这条记忆"
            }}</button
          ><button
            type="button"
            class="quiet"
            :disabled="locked"
            @click="confirmation = null"
          >
            取消变更
          </button>
        </div>
      </aside>
      <MemoryEditor
        v-if="showEditor && canEdit"
        :key="editorKey"
        :item="editing"
        :project="project.id"
        :branch="branch.id"
        :session="sessionID"
        :resources="resources"
        :items="items"
        :entities="branch.snapshot.entities"
        :blocked="locked"
        @save="saveItem"
        @dirty="editorDirty = $event"
        @busy="sourceBusy = $event"
        @expired="emit('expired')"
        @discard="discardEditor"
      />
    </div>
    <div v-show="activeSection === 'materials'" class="author-work-area">
      <form class="compile-form" @submit.prevent="compile">
        <h3>查看本次写作材料</h3>
        <fieldset :disabled="locked">
          <label
            >上下文用途<select v-model="purpose">
              <option value="author">作者规划</option>
              <option value="writer">正文写作</option>
              <option value="critic">独立审读</option>
              <option value="research">资料研究</option>
            </select></label
          >
          <label
            >当前角色视角<select
              v-model="perspective"
              :required="purpose === 'writer'"
            >
              <option value="">请选择角色（正文写作必选）</option>
              <option
                v-for="entity in branch.snapshot.entities"
                :key="entity.id"
                :value="entity.id"
              >
                {{ entity.name }}
              </option>
            </select></label
          >
          <label
            >上下文故事位置<input
              v-model.number="storyTime"
              type="number"
              required
              step="1"
          /></label>
          <label
            >上下文叙述位置<input
              v-model.number="narrativeTime"
              type="number"
              required
              step="1"
          /></label>
          <label
            >本次任务与检索词<textarea
              v-model="query"
              rows="2"
              maxlength="4000"
            />
          </label>
          <label
            >本次必须纳入的记忆<select v-model="requiredIDs" multiple>
              <option
                v-for="item in items.filter(
                  (i) => !['revoked', 'stale'].includes(i.state),
                )"
                :key="item.id"
                :value="item.id"
              >
                {{ memoryKinds[item.kind] }} · {{ item.text.slice(0, 65) }}
              </option>
            </select></label
          >
          <p class="muted">
            只使用已保存的条目。当前会话的未确认材料会明确标记；正文写作不会把它们当作正式事实。
          </p>
          <button>编译上下文</button>
        </fieldset>
      </form>
      <MemoryPackView
        v-if="pack"
        :pack="pack"
        :items="items"
        :resources="resources"
        :stale="stalePack"
        :blocked="locked"
        @source="viewSource"
      />
    </div>
    <aside v-if="sourceExcerpt" class="memory-card" data-testid="memory-source">
      <h3>{{ sourceExcerpt.title }}</h3>
      <p class="memory-text">{{ sourceExcerpt.text }}</p>
      <button
        v-for="pin in sourceExcerpt.item?.summary?.inputs ?? []"
        :key="pin.id"
        type="button"
        class="quiet"
        :disabled="locked"
        @click="viewSummary(pin)"
      >
        继续查看下层摘要 {{ pin.id.slice(0, 8) }}
      </button>
      <button
        v-for="(source, index) in sourceExcerpt.item?.sources ?? []"
        :key="index"
        type="button"
        class="quiet"
        :disabled="locked"
        @click="viewSource(source)"
      >
        查看原文片段 {{ index + 1 }}
      </button>
    </aside>
  </section>
</template>
<style scoped>
.memory-panel {
  display: grid;
  gap: 1rem;
}
.memory-card {
  padding: 1rem;
  border: 1px solid #d7ded5;
  border-radius: 8px;
}
.memory-text {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.memory-browser-controls {
  display: grid;
  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) minmax(0, 1fr);
  gap: 0.8rem;
}
@media (max-width: 760px) {
  .memory-browser-controls {
    grid-template-columns: 1fr;
  }
}
fieldset {
  display: grid;
  gap: 0.9rem;
  border: 1px solid #d7ded5;
  border-radius: 8px;
  min-width: 0;
}
</style>
