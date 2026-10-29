<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import {
  newOperationID,
  api,
  APIError,
  message,
  type Actor,
  type Project,
} from "../../api";
import type {
  Branch,
  Candidate,
  IPDraft,
  MergeView,
  Snapshot,
  BranchActionDraft,
  PromotionDraft,
} from "./types";
import WorldEditor from "./WorldEditor.vue";
import ProfileEditor from "./ProfileEditor.vue";
import CharacterLibrary from "./CharacterLibrary.vue";
import MappingEditor from "./MappingEditor.vue";
import PromotionPanel from "./PromotionPanel.vue";
import TaskPanel from "./TaskPanel.vue";
import KnowledgePanel from "./KnowledgePanel.vue";
import MemoryPanel from "./MemoryPanel.vue";
import GenerationPanel from "./GenerationPanel.vue";
import BundlePanel from "./BundlePanel.vue";
import IndexJobsPanel from "./IndexJobsPanel.vue";
import BranchDeltaPanel from "./BranchDeltaPanel.vue";
import SemanticReviewPanel from "./SemanticReviewPanel.vue";
import { activeReasons, type BranchDelta } from "./branchDelta";
import type { ResourceNavigationRequest } from "../novel/resourceNavigation";
import { useBoundedReads } from "../../composables/useBoundedReads";

const props = defineProps<{
  project: Project;
  actor: Actor;
  initialBranchId?: string;
  focusCharacters?: boolean;
  novelNavigation?: boolean;
  novelNavigationBusy?: boolean;
}>();
const characterLibrary = ref<InstanceType<typeof CharacterLibrary> | null>(
  null,
);
const emit = defineEmits<{
  close: [];
  expired: [];
  editResource: [request: ResourceNavigationRequest];
}>();
const root = `/projects/${props.project.id}/branches`;
const novelReads = useBoundedReads({
  stopped: "返回分支的重查已停止，原输入保持原样。",
  timeout: "返回分支的重查超时，原输入保持原样，可以重试。",
});
let novelReadSequence = 0;
const branches = ref<Branch[]>([]);
const current = ref<Branch | null>(null);
const candidates = ref<Candidate[]>([]);
const usableCandidates = computed(() =>
  candidates.value.filter((c) => !c.retention),
);
const publicationRevision = ref(0);
const indexCandidateID = ref("");
watch([() => current.value?.id, candidates], () => {
  if (
    !candidates.value.some(
      (c) =>
        c.id === indexCandidateID.value &&
        !c.retention &&
        c.status === "READY" &&
        c.base_head === current.value?.head,
    )
  )
    indexCandidateID.value = "";
});
const selectedChangePlan = ref<{ id: string; branch: string } | null>(null);
const selectedContext = ref<{
  id: string;
  purpose: string;
  branch: string;
} | null>(null);
const draft = ref<Snapshot>({ entities: [], assertions: [] });
const draftBase = ref("");
const amendment = ref(false);
const amendmentReason = ref("");
const changeReasons = ref<Record<string, string>>({});
const branchActions = ref<BranchActionDraft>({});
const bundlePanel = ref<InstanceType<typeof BundlePanel> | null>(null);
const deltaPreview = ref<BranchDelta | null>(null);
const deltaInput = ref("");
const deltaCurrent = computed(
  () =>
    !!deltaPreview.value && deltaInput.value === JSON.stringify(draft.value),
);
const reviewCandidate = ref("");
const semanticReady = ref<Record<string, boolean>>({});
const semanticBlocking = ref(false);
const branchName = ref("");
const source = ref("");
const busy = ref(false);
const dirty = ref(false);
const draftRevision = ref(0);
const draftSaved = ref(true);
const savingDraft = ref(false);
const draftProblem = ref("");
const bundleBlocking = ref(false);
const promotionBlocking = ref(false);
const generationBlocking = ref(false);
const memoryBlocking = ref(false);
const characterBlocking = ref(false);
const ownLeaveBlocked = computed(
  () =>
    busy.value ||
    savingDraft.value ||
    !draftSaved.value ||
    bundleBlocking.value ||
    promotionBlocking.value ||
    semanticBlocking.value ||
    generationBlocking.value ||
    memoryBlocking.value ||
    characterBlocking.value,
);
const leaveBlocked = computed(
  () => ownLeaveBlocked.value || !!props.novelNavigationBusy,
);
const mergeView = ref<MergeView | null>(null);
const choices = ref<Record<string, string>>({});
const mergeOperation = ref<string>(newOperationID());
const mergeDelta = ref<BranchDelta | null>(null);
const mergeReasons = ref<Record<string, string>>({});
let autosave: ReturnType<typeof setTimeout> | undefined;
let editSequence = 0;
let disposed = false;
const error = ref("");
const notice = ref("");
const operation = ref(newOperationID());
const branchOperation = ref(newOperationID());
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const canPublish = computed(
  () =>
    ["owner", "publisher"].includes(props.actor.role) &&
    ["owner", "publisher"].includes(props.project.role),
);
const canAdopt = computed(
  () => props.actor.role !== "reader" && props.project.role !== "reader",
);
const kindNames: Record<string, string> = {
  character: "角色",
  location: "地点",
  faction: "组织",
  ability: "能力",
  item: "物品",
  species: "种族",
  event: "事件实体",
  profession: "职业",
  cultivation: "修炼体系",
  technique: "功法",
  relationship: "关系实体",
  concept: "概念",
};
const assertionNames: Record<string, string> = {
  fact: "事实",
  belief: "信念",
  claim: "陈述",
  intent: "意图",
  hypothesis: "推测",
};
const statusNames = {
  PREPARED: "待确认",
  READY: "已采用，待发布",
  PUBLISHED: "已发布",
};
watch([branchName, source], () => {
  branchOperation.value = newOperationID();
});
function edited() {
  dirty.value = true;
  draftSaved.value = false;
  editSequence++;
  operation.value = newOperationID();
  queueDraft();
}
function queueDraft() {
  clearTimeout(autosave);
  autosave = setTimeout(() => {
    void persistDraft();
  }, 600);
}
async function persistDraft() {
  if (disposed || !current.value || !canEdit.value || draftSaved.value) return;
  if (busy.value || savingDraft.value) {
    queueDraft();
    return;
  }
  const branch = current.value.id;
  const sequence = editSequence;
  savingDraft.value = true;
  draftProblem.value = "";
  try {
    const saved = await api<IPDraft>(`${root}/${branch}/draft`, "PUT", {
      revision: draftRevision.value,
      base_head: draftBase.value,
      snapshot: JSON.parse(JSON.stringify(draft.value)),
      amendment: amendment.value,
      amendment_reason: amendmentReason.value,
      change_reasons: changeReasons.value,
      branch_actions: JSON.parse(JSON.stringify(branchActions.value)),
    });
    if (disposed || current.value?.id !== branch) return;
    draftRevision.value = saved.revision;
    draftSaved.value = sequence === editSequence;
    if (!draftSaved.value) queueDraft();
  } catch (e) {
    draftProblem.value = message(e);
  } finally {
    savingDraft.value = false;
  }
}
function guardUnload(event: BeforeUnloadEvent) {
  if (!draftSaved.value || savingDraft.value || characterBlocking.value) {
    event.preventDefault();
    event.returnValue = "";
  }
}
async function perform(work: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    busy.value = false;
  }
}
async function refreshBranches() {
  branches.value = (await api<{ items: Branch[] }>(root)).items;
}
function loadDraft(snapshot: Snapshot, base: string) {
  clearTimeout(autosave);
  draft.value = JSON.parse(JSON.stringify(snapshot));
  draft.value.entities ??= [];
  draft.value.assertions ??= [];
  draftBase.value = base;
  dirty.value = false;
  amendment.value = false;
  amendmentReason.value = "";
  changeReasons.value = {};
  deltaPreview.value = null;
  deltaInput.value = "";
  operation.value = newOperationID();
}
async function loadBranch(id: string) {
  const [branch, history, saved] = await Promise.all([
    api<Branch>(`${root}/${id}`),
    api<{ items: Candidate[] }>(`${root}/${id}/candidates`),
    api<IPDraft>(`${root}/${id}/draft`),
  ]);
  current.value = branch;
  candidates.value = history.items;
  loadDraft(branch.snapshot, branch.head);
  draftRevision.value = saved.revision;
  draftSaved.value = true;
  draftProblem.value = "";
  mergeView.value = null;
  branchActions.value = saved.branch_actions ?? {};
  if (saved.revision && canEdit.value) {
    loadDraft(saved.snapshot, saved.base_head);
    amendment.value = saved.amendment;
    amendmentReason.value = saved.amendment_reason;
    changeReasons.value = saved.change_reasons ?? {};
    dirty.value =
      JSON.stringify(saved.snapshot) !== JSON.stringify(branch.snapshot) ||
      saved.base_head !== branch.head;
    if (dirty.value) notice.value = "已恢复你上次暂存的草稿，尚未采用。";
  }
}
async function choose(id: string) {
  await perform(() => loadBranch(id));
}
async function useChangePlan(value: { id: string; branch: string }) {
  if (current.value?.id === value.branch) {
    selectedChangePlan.value = value;
    return;
  }
  if (leaveBlocked.value) {
    error.value = "请先保存或放弃当前未完成的编辑，再切换分支处理。";
    return;
  }
  await perform(async () => {
    await loadBranch(value.branch);
    selectedChangePlan.value = value;
  });
}
function canOpenNovelResource() {
  return (
    !!props.novelNavigation &&
    canEdit.value &&
    !ownLeaveBlocked.value &&
    !!current.value &&
    (!props.initialBranchId || current.value.id === props.initialBranchId)
  );
}
function editNovelResource(request: ResourceNavigationRequest) {
  if (
    !canOpenNovelResource() ||
    leaveBlocked.value ||
    request.ref.kind !== "resource" ||
    request.ref.branch_id !== current.value?.id ||
    (!!props.initialBranchId && request.ref.branch_id !== props.initialBranchId)
  )
    return;
  emit("editResource", request);
}
async function afterNovelEdit() {
  await nextTick();
  if (
    !props.novelNavigation ||
    disposed ||
    leaveBlocked.value ||
    !current.value
  )
    return;
  const id = current.value.id;
  const sequence = ++novelReadSequence;
  const projectID = props.project.id,
    actorID = props.actor.id;
  const active = () =>
    !disposed &&
    novelReads.active() &&
    sequence === novelReadSequence &&
    props.project.id === projectID &&
    props.actor.id === actorID &&
    current.value?.id === id;
  await bundlePanel.value?.afterNovelEdit();
  await nextTick();
  if (!active()) return;
  await perform(async () => {
    try {
      const [branch, history, all] = await Promise.all([
        novelReads.read<Branch>(`${root}/${id}`),
        novelReads.read<{ items: Candidate[] }>(`${root}/${id}/candidates`),
        novelReads.read<{ items: Branch[] }>(root),
      ]);
      if (!active()) return;
      current.value = branch;
      candidates.value = history.items;
      branches.value = all.items;
      notice.value =
        "已重读原分支版本；原作者输入仍保留，未重新生成清单或审查。";
    } catch (e) {
      if (!active()) return;
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        current.value = null;
        branches.value = [];
        candidates.value = [];
        draft.value = { entities: [], assertions: [] };
        mergeView.value = null;
        deltaPreview.value = null;
        selectedContext.value = null;
        selectedChangePlan.value = null;
      }
      throw e;
    }
  });
}
defineExpose({ afterNovelEdit, canOpenNovelResource });
async function createBranch() {
  await perform(async () => {
    const parent = branches.value.find((b) => b.id === source.value);
    const branch = await api<Branch>(root, "POST", {
      name: branchName.value,
      parent_id: parent?.id ?? "",
      base_revision: parent?.head ?? "",
      operation_id: branchOperation.value,
    });
    branchName.value = "";
    await refreshBranches();
    await loadBranch(branch.id);
    notice.value = parent
      ? "改编分支已从固定的正式版本建立"
      : "独立分支已建立，可以开始整理设定";
  });
}
function addEntity() {
  draft.value.entities.push({
    id: crypto.randomUUID(),
    kind: "character",
    name: "",
    aliases: [],
    description: "",
  });
  edited();
}
function focusAssertion(id: string) {
  const editor = document.getElementById(`ip-assertion-${id}`);
  editor?.scrollIntoView({ behavior: "smooth", block: "center" });
  editor
    ?.querySelector<HTMLElement>(
      "input:not(:disabled), select:not(:disabled), textarea:not(:disabled)",
    )
    ?.focus({ preventScroll: true });
}
function removeEntity(id: string) {
  if (
    draft.value.assertions.some(
      (a) =>
        a.subject === id ||
        a.object_id === id ||
        a.holder_id === id ||
        a.known_by?.includes(id),
    ) ||
    draft.value.rules?.some(
      (r) =>
        r.subject === id ||
        r.when?.some((c) => c.subject === id) ||
        r.exceptions?.some((e) => e.when.some((c) => c.subject === id)),
    ) ||
    draft.value.events?.some((e) => e.participants.includes(id)) ||
    draft.value.mappings?.some(
      (m) => m.target === id || m.sources.includes(id),
    ) ||
    draft.value.entities.some(
      (e) =>
        e.id !== id &&
        (e.profile?.related?.includes(id) ||
          e.profile?.requirements.some((r) => r.subject === id)),
    )
  ) {
    error.value = "此实体仍被设定或关系引用，请先处理相关设定。";
    return;
  }
  draft.value.entities = draft.value.entities.filter((e) => e.id !== id);
  edited();
}
function addAssertion() {
  draft.value.assertions.push({
    id: crypto.randomUUID(),
    subject: draft.value.entities[0]?.id ?? "",
    predicate: "",
    value: "",
    kind: "fact",
    effective_from: 0,
    known_from: 0,
    known_by: [],
    visibility: "public",
    recorded_at: "",
    locked: false,
    source: "作者输入",
  });
  edited();
}
async function save() {
  if (!current.value || savingDraft.value) return;
  const path = `${root}/${current.value.id}`;
  await perform(async () => {
    const encoded = JSON.stringify(draft.value);
    if (!deltaPreview.value || deltaInput.value !== encoded) {
      deltaPreview.value = await api<BranchDelta>(
        `${path}/delta-preview`,
        "POST",
        { base_head: draftBase.value, snapshot: draft.value },
      );
      deltaInput.value = encoded;
      if (deltaPreview.value.operations.length) {
        notice.value =
          "逐项变化已列出。请填写每项理由，再保存候选；未完成内容已自动暂存。";
        return;
      }
    }
    await api<Candidate>(
      `${path}/${amendment.value ? "amendments" : "candidates"}`,
      "POST",
      {
        base_head: draftBase.value,
        operation_id: operation.value,
        snapshot: draft.value,
        change_reasons: activeReasons(deltaPreview.value, changeReasons.value),
        ...(amendment.value ? { reason: amendmentReason.value } : {}),
      },
    );
    candidates.value = (
      await api<{ items: Candidate[] }>(`${path}/candidates`)
    ).items;
    dirty.value = false;
    notice.value = "候选已保存，正式设定尚未改变。请检查后确认采用。";
  });
}
function revise(candidate: Candidate) {
  if (!current.value || candidate.retention) return;
  loadDraft(candidate.snapshot, current.value.head);
  amendment.value = !!candidate.amendment_reason;
  amendmentReason.value = candidate.amendment_reason;
  changeReasons.value = Object.fromEntries(
    (candidate.delta?.operations ?? [])
      .filter((op) => op.field !== "/recorded_at")
      .map((op) => [
        `${op.object_kind}:${op.object_id}:${op.field}`,
        op.reason,
      ]),
  );
  edited();
  notice.value = "候选内容已载入编辑区；修改后会保存为另一个候选。";
}
async function advance(
  candidate: Candidate,
  action: "adopt" | "publish" | "approve-amendment",
) {
  if (!current.value || savingDraft.value || candidate.retention) return;
  const id = current.value.id;
  await perform(async () => {
    if (action === "publish")
      notice.value = "正在校验并准备此版本的检索索引，完成后发布。";
    await api(`${root}/${id}/candidates/${candidate.id}/${action}`, "POST", {});
    if (action === "publish") selectedContext.value = null;
    // Keep unsaved local editing intact while refreshing publication status.
    const branch = await api<Branch>(`${root}/${id}`);
    current.value = branch;
    candidates.value = (
      await api<{ items: Candidate[] }>(`${root}/${id}/candidates`)
    ).items;
    await refreshBranches();
    if (!dirty.value && action === "publish") {
      loadDraft(branch.snapshot, branch.head);
      draftSaved.value = false;
      editSequence++;
      queueDraft();
    }
    notice.value =
      action === "publish"
        ? "正式版本已发布，旧版本仍保留。"
        : "已确认采用；正式内容将在发布后切换。";
  });
}
function discard() {
  if (!current.value) return;
  loadDraft(current.value.snapshot, current.value.head);
  edited();
  dirty.value = false;
}
async function refreshPublished() {
  if (!current.value) return;
  publicationRevision.value++;
  selectedContext.value = null;
  const id = current.value.id;
  try {
    const branch = await api<Branch>(`${root}/${id}`);
    const history = await api<{ items: Candidate[] }>(
      `${root}/${id}/candidates`,
    );
    if (current.value?.id !== id || disposed) return;
    current.value = branch;
    candidates.value = history.items;
    await refreshBranches();
    if (!dirty.value && !savingDraft.value) {
      loadDraft(branch.snapshot, branch.head);
      draftSaved.value = false;
      editSequence++;
      queueDraft();
    }
  } catch (e) {
    error.value = message(e);
  }
}
async function compareUpstream() {
  if (!current.value) return;
  await perform(async () => {
    mergeView.value = await api<MergeView>(
      `${root}/${current.value!.id}/merge-preview`,
    );
    const saved = branchActions.value.merge;
    const matching =
      saved?.base_head === mergeView.value.base_head &&
      saved?.upstream_head === mergeView.value.upstream_head;
    choices.value = matching ? { ...saved.resolutions } : {};
    mergeDelta.value = null;
    mergeReasons.value = { ...saved?.change_reasons };
    mergeOperation.value = matching ? saved.operation_id : newOperationID();
    if (saved)
      notice.value = matching
        ? "已恢复此前的合并选择和理由，请检查后继续。"
        : "分支版本已变化，旧理由仍保留供对照；请重新选择冲突处理。";
  });
}
function describeConflict(
  snapshot: Snapshot,
  type: string,
  id: string,
): string {
  switch (type) {
    case "entity": {
      const e = snapshot.entities.find((e) => e.id === id);
      return e ? `${e.name}：${e.description}` : "已删除";
    }
    case "assertion": {
      const a = snapshot.assertions.find((a) => a.id === id);
      return a
        ? `${a.predicate}：${a.value}（${assertionNames[a.kind]}）`
        : "已删除";
    }
    case "rule": {
      const r = snapshot.rules?.find((r) => r.id === id);
      return r ? `${r.name}：${r.predicate} ${r.operand}` : "已删除";
    }
    case "event": {
      const e = snapshot.events?.find((e) => e.id === id);
      return e ? `${e.name}：${e.description}` : "已删除";
    }
    case "mapping": {
      const m = snapshot.mappings?.find((m) => m.id === id);
      const name = (id: string) =>
        snapshot.entities.find((e) => e.id === id)?.name ?? id;
      return m
        ? `${name(m.target)} ← ${m.sources.map(name).join("、")}：${m.reason}（${m.kind}）`
        : "已删除";
    }
    default:
      return "";
  }
}
function changeMergeChoice() {
  mergeOperation.value = newOperationID();
  mergeDelta.value = null;
  saveMergeDraft();
}
function saveMergeDraft() {
  if (!mergeView.value) return;
  branchActions.value.merge = {
    base_head: mergeView.value.base_head,
    upstream_head: mergeView.value.upstream_head,
    operation_id: mergeOperation.value,
    resolutions: { ...choices.value },
    change_reasons: { ...mergeReasons.value },
  };
  edited();
}
function savePromotionDraft(value: PromotionDraft) {
  branchActions.value.promotion = value;
  edited();
}
async function saveMerge() {
  if (!current.value || !mergeView.value) return;
  const id = current.value.id;
  const view = mergeView.value;
  await perform(async () => {
    if (!mergeDelta.value) {
      mergeDelta.value = await api<BranchDelta>(
        `${root}/${id}/merge-delta-preview`,
        "POST",
        {
          base_head: view.base_head,
          upstream_head: view.upstream_head,
          operation_id: mergeOperation.value,
          resolutions: choices.value,
        },
      );
      if (mergeDelta.value.operations.length) {
        notice.value = "请逐项说明合并理由，再保存候选。";
        return;
      }
    }
    await api<Candidate>(`${root}/${id}/merge`, "POST", {
      base_head: view.base_head,
      upstream_head: view.upstream_head,
      operation_id: mergeOperation.value,
      resolutions: choices.value,
      change_reasons: activeReasons(mergeDelta.value, mergeReasons.value),
    });
    candidates.value = (
      await api<{ items: Candidate[] }>(`${root}/${id}/candidates`)
    ).items;
    mergeView.value = null;
    delete branchActions.value.merge;
    edited();
    notice.value = "合并结果已保存为候选，请检查后确认采用与发布。";
  });
}
onMounted(() => {
  window.addEventListener("beforeunload", guardUnload);
  void perform(async () => {
    await refreshBranches();
    if (
      props.initialBranchId &&
      branches.value.some((branch) => branch.id === props.initialBranchId)
    )
      await loadBranch(props.initialBranchId);
    await nextTick();
    if (!disposed && props.focusCharacters)
      characterLibrary.value?.focusSearch();
  });
});
onUnmounted(() => {
  disposed = true;
  clearTimeout(autosave);
  window.removeEventListener("beforeunload", guardUnload);
});
</script>

<template>
  <main class="workspace ip-workspace">
    <div class="intro">
      <div>
        <p class="eyebrow">{{ project.name }} / IP LIBRARY</p>
        <h1>共享设定库</h1>
        <p class="muted">
          正史与改编分别维护。角色身份、事实和知情时间随版本保留。
        </p>
      </div>
      <button class="quiet" :disabled="leaveBlocked" @click="emit('close')">
        返回项目书架
      </button>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p v-if="dirty && draftSaved" class="notice">草稿已自动暂存，尚未采用</p>
    <p v-else-if="!draftSaved || savingDraft" class="muted">正在暂存草稿…</p>
    <p v-if="draftProblem" class="error" role="alert">
      草稿暂存未完成：{{ draftProblem }}
      <button
        class="quiet"
        :disabled="busy || savingDraft"
        @click="persistDraft"
      >
        重试暂存
      </button>
    </p>
    <section class="panel branch-panel">
      <h2>正史与改编分支</h2>
      <nav class="actions" aria-label="设定分支">
        <button
          v-for="branch in branches"
          :key="branch.id"
          :class="{ quiet: branch.id !== current?.id }"
          :disabled="leaveBlocked"
          @click="choose(branch.id)"
        >
          {{ branch.name }}
        </button>
      </nav>
      <form v-if="canEdit" class="branch-create" @submit.prevent="createBranch">
        <label
          >分支名称<input
            v-model="branchName"
            required
            maxlength="120"
            :disabled="leaveBlocked"
        /></label>
        <label
          >分支来源<select v-model="source" :disabled="leaveBlocked">
            <option value="">独立正史</option>
            <option
              v-for="branch in branches.filter((b) => b.head)"
              :key="branch.id"
              :value="branch.id"
            >
              {{ branch.name }}（当前已发布版本）
            </option>
          </select></label
        >
        <button :disabled="leaveBlocked">创建分支</button>
      </form>
    </section>
    <template v-if="current">
      <section v-if="current.parent_id" class="panel">
        <h2>与上游比较</h2>
        <p class="muted">
          比较分支建立时的基础版本、上游当前版本和本分支版本。合并只生成待确认候选。
        </p>
        <button :disabled="leaveBlocked" @click="compareUpstream">
          比较上游变化
        </button>
        <template v-if="mergeView">
          <p v-if="!mergeView.conflicts.length" class="notice">
            没有需要手动解决的双向修改，仍需检查合并后的候选。
          </p>
          <article
            v-for="conflict in mergeView.conflicts"
            :key="conflict.type + conflict.assertion_id"
            class="candidate-card"
          >
            <h3>{{ conflict.reason }}</h3>
            <p>
              共同基础：{{
                describeConflict(
                  mergeView.base,
                  conflict.type,
                  conflict.assertion_id,
                )
              }}
            </p>
            <p>
              上游当前：{{
                describeConflict(
                  mergeView.upstream,
                  conflict.type,
                  conflict.assertion_id,
                )
              }}
            </p>
            <p>
              本地当前：{{
                describeConflict(
                  mergeView.local,
                  conflict.type,
                  conflict.assertion_id,
                )
              }}
            </p>
            <label
              >冲突处理<select
                v-model="choices[conflict.type + ':' + conflict.assertion_id]"
                :disabled="busy"
                @change="changeMergeChoice"
              >
                <option value="">请选择</option>
                <option value="local">保留本地修改</option>
                <option value="upstream">采用上游修改</option>
              </select></label
            >
          </article>
          <button
            v-if="canEdit"
            :disabled="
              leaveBlocked ||
              mergeView.conflicts.some(
                (c) => !choices[c.type + ':' + c.assertion_id],
              )
            "
            @click="saveMerge"
          >
            保存合并候选
          </button>
          <BranchDeltaPanel
            v-if="mergeDelta"
            v-model="mergeReasons"
            :delta="mergeDelta"
            :disabled="busy"
            @update:model-value="saveMergeDraft"
          />
        </template>
      </section>
      <CharacterLibrary
        ref="characterLibrary"
        :key="current.id"
        :project-id="project.id"
        :branch-id="current.id"
        :branch-head="current.head"
        :can-publish="canPublish"
        :snapshot="canEdit ? draft : current.snapshot"
        :can-edit="canEdit"
        :disabled="busy"
        :locked-entity-ids="
          amendment
            ? []
            : current.snapshot.entities.filter((e) => e.locked).map((e) => e.id)
        "
        @edited="edited"
        @edit-assertion="focusAssertion"
        @blocking="characterBlocking = $event"
      />
      <section class="panel published-panel" data-testid="published-snapshot">
        <h2>{{ current.name }} · 正式设定</h2>
        <p class="muted">
          {{ current.head ? `已发布 · ${current.head.slice(0, 8)}` : "尚未发布"
          }}<span v-if="current.parent_id">
            · 基于上游固定版本 {{ current.base_revision.slice(0, 8) }}</span
          >
        </p>
        <p v-if="!current.snapshot.entities?.length">正式版本尚无实体</p>
        <article
          v-for="entity in current.snapshot.entities"
          :key="entity.id"
          class="published-entity"
        >
          <strong>{{ entity.name }}</strong
          ><span class="pill">{{ kindNames[entity.kind] }}</span>
          <p>{{ entity.description }}</p>
          <ul>
            <li
              v-for="a in current.snapshot.assertions.filter(
                (a) => a.subject === entity.id,
              )"
              :key="a.id"
            >
              {{ assertionNames[a.kind] }} · {{ a.predicate }}：{{ a.value }}
              <span v-if="a.context && a.context !== 'reality'"
                >（{{
                  a.context === "dream"
                    ? "梦境"
                    : a.context === "rumor"
                      ? "传言"
                      : a.context === "flashback"
                        ? "倒叙"
                        : "假设"
                }}）</span
              >
              <span v-if="a.status === 'retracted'">（已撤回）</span
              ><span v-if="a.status === 'disputed'">（有争议）</span>
              <span v-if="a.locked">（已锁定）</span>
            </li>
          </ul>
        </article>
        <h3 v-if="current.snapshot.rules?.length">世界规则</h3>
        <p v-for="rule in current.snapshot.rules" :key="rule.id">
          {{ rule.name }} · {{ rule.predicate }}：{{ rule.operand }} · 来源
          {{ rule.source }}
        </p>
        <h3 v-if="current.snapshot.events?.length">故事时间线</h3>
        <p
          v-for="event in [...(current.snapshot.events ?? [])].sort(
            (a, b) => a.time - b.time,
          )"
          :key="event.id"
        >
          {{ event.time }} · {{ event.name }}：{{ event.description }}
        </p>
      </section>
      <section v-if="canEdit" class="panel draft-panel">
        <h2>编辑下一个版本</h2>
        <p class="muted">
          事实、人物信念、对外陈述分别记录。时间使用故事中的顺序位置，0
          表示故事开始之前。
        </p>
        <form @submit.prevent="save" @input="edited" @change="edited">
          <label class="checkbox-label"
            ><input
              v-model="amendment"
              type="checkbox"
              :disabled="busy"
            />提出锁定设定修订（须负责人专门批准）</label
          >
          <label v-if="amendment"
            >修订理由<textarea
              v-model="amendmentReason"
              rows="3"
              required
              maxlength="2000"
              :disabled="busy"
            ></textarea>
          </label>
          <fieldset
            v-for="(entity, index) in draft.entities"
            :key="entity.id"
            :disabled="
              busy ||
              (!amendment &&
                !!current.snapshot.entities.find(
                  (old) => old.id === entity.id && old.locked,
                ))
            "
            class="entity-editor"
          >
            <legend>实体 {{ index + 1 }}</legend>
            <div class="field-grid">
              <label>实体名称<input v-model="entity.name" required /></label
              ><label
                >实体类型<select v-model="entity.kind">
                  <option
                    v-for="(label, key) in kindNames"
                    :key="key"
                    :value="key"
                  >
                    {{ label }}
                  </option>
                </select></label
              >
            </div>
            <label
              >实体简介<textarea
                v-model="entity.description"
                rows="3"
              ></textarea>
            </label>
            <div class="field-grid">
              <label
                >别名（逗号分隔）<input
                  :value="entity.aliases?.join('，')"
                  @input="
                    entity.aliases = ($event.target as HTMLInputElement).value
                      .split(/[,，]/)
                      .map((v) => v.trim())
                      .filter(Boolean)
                  " /></label
              ><label
                >使用状态<select v-model="entity.status">
                  <option value="">正常使用</option>
                  <option value="disabled">停用，保留引用</option>
                </select></label
              >
            </div>
            <ProfileEditor
              :entity="entity"
              :entities="draft.entities"
              @edited="edited"
            />
            <label class="checkbox-label"
              ><input
                v-model="entity.locked"
                type="checkbox"
              />锁定此实体与结构化定义</label
            >
            <button
              type="button"
              class="quiet"
              @click="removeEntity(entity.id)"
            >
              从候选移除此实体
            </button>
          </fieldset>
          <button
            type="button"
            class="quiet"
            :disabled="busy"
            @click="addEntity"
          >
            新增角色或实体
          </button>
          <fieldset
            v-for="(a, index) in draft.assertions"
            :key="a.id"
            :id="`ip-assertion-${a.id}`"
            :disabled="
              busy ||
              (!amendment &&
                !!current.snapshot.assertions?.find(
                  (old) => old.id === a.id && old.locked,
                ))
            "
            class="entity-editor"
          >
            <legend>
              设定 {{ index + 1 }} <span v-if="a.locked">· 锁定</span>
            </legend>
            <div class="field-grid">
              <label
                >设定主体<select v-model="a.subject" required>
                  <option value="">请选择</option>
                  <option v-for="e in draft.entities" :key="e.id" :value="e.id">
                    {{ e.name || "未命名实体" }}
                  </option>
                </select></label
              ><label
                >命题类别<select v-model="a.kind">
                  <option
                    v-for="(label, key) in assertionNames"
                    :key="key"
                    :value="key"
                  >
                    {{ label }}
                  </option>
                </select></label
              ><label>设定属性<input v-model="a.predicate" required /></label
              ><label>设定内容<input v-model="a.value" required /></label
              ><label
                >生效时间<input
                  v-model.number="a.effective_from"
                  type="number"
                  step="1"
                  required /></label
              ><label
                >知情时间<input
                  v-model.number="a.known_from"
                  type="number"
                  step="1"
                  required /></label
              ><label
                >可见范围<select v-model="a.visibility">
                  <option value="public">故事内公开</option>
                  <option value="author">仅作者知道</option>
                  <option value="restricted">指定角色知道</option>
                </select></label
              ><label>来源说明<input v-model="a.source" required /></label>
              <label
                >关系对象（可选）<select v-model="a.object_id">
                  <option value="">无</option>
                  <option
                    v-for="entity in draft.entities"
                    :key="entity.id"
                    :value="entity.id"
                  >
                    {{ entity.name }}
                  </option>
                </select></label
              >
            </div>
            <div class="field-grid">
              <label v-if="['belief', 'claim', 'intent'].includes(a.kind)"
                >谁相信、说出或计划<select
                  v-model="a.holder_id"
                  required
                  aria-label="谁相信、说出或计划"
                >
                  <option value="">请选择</option>
                  <option v-for="e in draft.entities" :key="e.id" :value="e.id">
                    {{ e.name }}
                  </option>
                </select></label
              >
              <label
                >叙述语境<select v-model="a.context" aria-label="叙述语境">
                  <option :value="undefined">现实</option>
                  <option value="reality">现实</option>
                  <option value="flashback">倒叙中的现实</option>
                  <option value="dream">梦境</option>
                  <option value="rumor">传言</option>
                  <option value="hypothetical">假设</option>
                </select></label
              >
              <label
                >命题状态<select v-model="a.status" aria-label="命题状态">
                  <option :value="undefined">有效</option>
                  <option value="active">有效</option>
                  <option value="disputed">有争议</option>
                  <option value="retracted">已撤回</option>
                </select></label
              >
              <label
                >置信度（可选，0—1）<input
                  :value="a.confidence"
                  type="number"
                  min="0"
                  max="1"
                  step="0.01"
                  @input="
                    a.confidence =
                      ($event.target as HTMLInputElement).value === ''
                        ? null
                        : Number(($event.target as HTMLInputElement).value)
                  "
              /></label>
              <label
                >结束时间（可选，不含此时刻）<input
                  :value="a.effective_until"
                  type="number"
                  step="1"
                  @input="
                    a.effective_until =
                      ($event.target as HTMLInputElement).value === ''
                        ? null
                        : Number(($event.target as HTMLInputElement).value)
                  "
              /></label>
            </div>
            <label v-if="a.visibility === 'restricted'"
              >知情角色<select v-model="a.known_by" multiple>
                <option
                  v-for="e in draft.entities.filter(
                    (e) => e.kind === 'character',
                  )"
                  :key="e.id"
                  :value="e.id"
                >
                  {{ e.name }}
                </option>
              </select></label
            >
            <label class="checkbox-label"
              ><input
                v-model="a.locked"
                type="checkbox"
              />锁定此设定，发布后禁止直接改写</label
            >
            <button
              type="button"
              class="quiet"
              @click="
                draft.assertions.splice(index, 1);
                edited();
              "
            >
              从候选移除此设定
            </button>
          </fieldset>
          <button
            type="button"
            class="quiet"
            :disabled="busy || !draft.entities.length"
            @click="addAssertion"
          >
            新增设定断言
          </button>
          <WorldEditor
            :snapshot="draft"
            :published="current.snapshot"
            :busy="busy"
            :amendment="amendment"
            @edited="edited"
          />
          <MappingEditor :snapshot="draft" :busy="busy" @edited="edited" />
          <BranchDeltaPanel
            v-if="deltaPreview && deltaCurrent"
            v-model="changeReasons"
            :delta="deltaPreview"
            :disabled="busy"
          />
          <div class="actions">
            <button :disabled="busy || savingDraft || !dirty">
              {{
                deltaCurrent ? "保存为候选版本" : "检查变更并保存候选"
              }}</button
            ><button
              type="button"
              class="quiet"
              :disabled="busy || savingDraft || !dirty"
              @click="discard"
            >
              放弃本次编辑
            </button>
          </div>
        </form>
      </section>
      <PromotionPanel
        v-if="canEdit"
        :key="current.id + '-promotion'"
        :project="project.id"
        :branch="current"
        :branches="branches"
        :draft="branchActions.promotion"
        :blocked="busy || savingDraft || bundleBlocking"
        @created="refreshPublished"
        @blocking="promotionBlocking = $event"
        @draft="savePromotionDraft"
      />
      <BundlePanel
        ref="bundlePanel"
        :key="current.id"
        :project="project"
        :actor="actor"
        :branch="current"
        :candidates="usableCandidates"
        :novel-navigation="
          novelNavigation &&
          (!initialBranchId || current.id === initialBranchId)
        "
        :blocked="
          busy || savingDraft || memoryBlocking || !!novelNavigationBusy
        "
        @published="refreshPublished"
        @change-plan="useChangePlan"
        @edit-resource="editNovelResource"
        @blocking="bundleBlocking = $event"
        @expired="emit('expired')"
      />
      <MemoryPanel
        :key="current.id + '-memory-' + actor.id"
        :project="project"
        :actor="actor"
        :branch="current"
        :blocked="busy || savingDraft || bundleBlocking"
        :publication-revision="publicationRevision"
        @blocking="memoryBlocking = $event"
        @expired="emit('expired')"
      />
      <KnowledgePanel
        :key="current.id + '-knowledge'"
        :project="project"
        :actor="actor"
        :branch="current"
        :blocked="busy || savingDraft || bundleBlocking"
        :publication-revision="publicationRevision"
        @use="selectedContext = $event"
        @expired="emit('expired')"
      />
      <GenerationPanel
        :key="current.id + '-generation'"
        :project="project"
        :actor="actor"
        :branch="current"
        :change-plan="
          selectedChangePlan?.branch === current.id ? selectedChangePlan : null
        "
        :context="
          selectedContext?.branch === current.id ? selectedContext : null
        "
        @expired="emit('expired')"
        @blocking="generationBlocking = $event"
        @prepared="bundlePanel?.refreshAfterPrepared()"
      />
      <TaskPanel
        :key="current.id + '-tasks'"
        :project="project"
        :actor="actor"
        :branch="current"
        :candidates="usableCandidates"
        :blocked="busy || savingDraft"
        @expired="emit('expired')"
      />
      <section class="panel history-panel">
        <h2>候选与版本记录</h2>
        <template
          v-if="
            canPublish &&
            candidates.some(
              (c) =>
                !c.retention &&
                c.status === 'READY' &&
                c.base_head === current?.head,
            )
          "
          ><label
            >预建发布索引的设定候选<select
              v-model="indexCandidateID"
              aria-label="预建发布索引的设定候选"
            >
              <option value="">选择已采用候选</option>
              <option
                v-for="c in candidates.filter(
                  (c) =>
                    !c.retention &&
                    c.status === 'READY' &&
                    c.base_head === current?.head,
                )"
                :key="c.id"
                :value="c.id"
              >
                {{
                  c.snapshot.entities.map((e) => e.name).join("、") || "空设定"
                }}
                · {{ c.id.slice(0, 8) }}
              </option>
            </select></label
          ><IndexJobsPanel
            v-if="indexCandidateID"
            :key="current.id + indexCandidateID"
            :project="project"
            :actor="actor"
            :branch="current"
            :candidate-id="indexCandidateID"
            :blocked="busy || savingDraft"
            label="后台准备此设定的发布索引"
            @expired="emit('expired')"
        /></template>
        <p v-if="!candidates.length" class="muted">还没有保存候选。</p>
        <article
          v-for="candidate in candidates"
          :key="candidate.id"
          class="candidate-card"
        >
          <h3>
            {{ statusNames[candidate.status] }} · {{ candidate.id.slice(0, 8) }}
          </h3>
          <p v-if="candidate.retention" class="notice">
            此候选的变更记录已过期清理，设定快照仍可查看，不能继续编辑、采用或发布。
          </p>
          <p v-if="candidate.amendment_reason" class="notice">
            锁定修订提案：{{ candidate.amendment_reason }}
          </p>
          <p v-if="candidate.promotion" class="notice">
            改编回流提案：{{ candidate.promotion.reason }}。须由负责人确认采用。
          </p>
          <p>
            {{
              candidate.snapshot.entities?.map((e) => e.name).join("、") ||
              "空设定"
            }}
            · {{ candidate.snapshot.assertions?.length ?? 0 }} 条设定
          </p>
          <details>
            <summary>检查此版本的具体内容</summary>
            <p
              v-for="check in candidate.checks"
              :key="check.rule_id + check.story_time"
              :class="
                ['SATISFIED', 'NOT_APPLICABLE', 'EXEMPT'].includes(check.status)
                  ? 'notice'
                  : 'error'
              "
            >
              规则检查 · 时间 {{ check.story_time }}：{{ check.reason }}
            </p>
            <details v-if="candidate.delta">
              <summary>查看逐项变更与理由</summary>
              <BranchDeltaPanel :delta="candidate.delta" readonly />
            </details>
            <p v-else class="muted">
              旧候选未记录逐项理由；保留原历史，不补造理由。
            </p>
            <article v-for="e in candidate.snapshot.entities" :key="e.id">
              <h4>{{ e.name }}</h4>
              <p>{{ e.description }}</p>
              <ul>
                <li
                  v-for="a in candidate.snapshot.assertions.filter(
                    (a) => a.subject === e.id,
                  )"
                  :key="a.id"
                >
                  {{ assertionNames[a.kind] }} · {{ a.predicate }}：{{
                    a.value
                  }}；生效 {{ a.effective_from }}，知情 {{ a.known_from }} ·
                  记录于 {{ a.recorded_at || "待保存" }}
                </li>
              </ul>
            </article>
            <p v-for="rule in candidate.snapshot.rules" :key="rule.id">
              规则 {{ rule.name }} · {{ rule.predicate }}：{{ rule.operand }}
            </p>
            <p v-for="event in candidate.snapshot.events" :key="event.id">
              事件 {{ event.time }} · {{ event.name }}：{{ event.description }}
            </p>
          </details>
          <p
            v-if="
              !candidate.retention &&
              candidate.status !== 'PUBLISHED' &&
              candidate.base_head !== current.head
            "
            class="error"
          >
            基础版本已变化，需重新编辑并核对。
          </p>
          <button
            v-if="
              !candidate.retention &&
              candidate.status !== 'PUBLISHED' &&
              candidate.base_head === current.head
            "
            :disabled="busy || savingDraft || bundleBlocking"
            @click="bundlePanel?.previewCandidateImpact(candidate.id)"
          >
            查看这次变更的影响
          </button>
          <button
            v-if="!candidate.retention && candidate.status !== 'PUBLISHED'"
            :disabled="busy || savingDraft || semanticBlocking"
            @click="
              reviewCandidate =
                reviewCandidate === candidate.id ? '' : candidate.id
            "
          >
            {{
              reviewCandidate === candidate.id
                ? "收起独立审查"
                : "打开独立语义审查"
            }}
          </button>
          <SemanticReviewPanel
            v-if="
              reviewCandidate === candidate.id &&
              !candidate.retention &&
              candidate.status !== 'PUBLISHED'
            "
            :key="candidate.id"
            :project="project"
            :actor="actor"
            :branch="current.id"
            target-kind="ip_candidate"
            :target-i-d="candidate.id"
            :target-hash="candidate.hash"
            :required="!!candidate.requires_semantic_review"
            :blocked="busy || savingDraft"
            @ready="semanticReady[candidate.id] = $event"
            @review-required="candidate.requires_semantic_review ||= $event"
            @blocking="semanticBlocking = $event"
            @expired="emit('expired')"
          />
          <div v-if="!candidate.retention" class="actions">
            <button
              v-if="canEdit"
              class="quiet"
              :disabled="busy || dirty"
              @click="revise(candidate)"
            >
              载入编辑</button
            ><button
              v-if="
                canAdopt &&
                candidate.status === 'PREPARED' &&
                !candidate.amendment_reason &&
                (!candidate.promotion ||
                  (actor.role === 'owner' && project.role === 'owner'))
              "
              :disabled="
                busy ||
                savingDraft ||
                candidate.base_head !== current.head ||
                (!!candidate.requires_semantic_review &&
                  !semanticReady[candidate.id])
              "
              @click="advance(candidate, 'adopt')"
            >
              确认采用此候选</button
            ><button
              v-if="
                actor.role === 'owner' &&
                project.role === 'owner' &&
                candidate.status === 'PREPARED' &&
                candidate.amendment_reason
              "
              :disabled="
                busy ||
                savingDraft ||
                candidate.base_head !== current.head ||
                (!!candidate.requires_semantic_review &&
                  !semanticReady[candidate.id])
              "
              @click="advance(candidate, 'approve-amendment')"
            >
              批准锁定修订并采用</button
            ><button
              v-if="canPublish && candidate.status === 'READY'"
              :disabled="
                busy ||
                savingDraft ||
                candidate.base_head !== current.head ||
                (!!candidate.requires_semantic_review &&
                  !semanticReady[candidate.id])
              "
              @click="advance(candidate, 'publish')"
            >
              发布此版本
            </button>
          </div>
        </article>
      </section>
    </template>
  </main>
</template>
