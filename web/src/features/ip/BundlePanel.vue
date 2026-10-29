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
import type { Branch, Candidate } from "./types";
import type { ResourceNavigationRequest } from "../novel/resourceNavigation";
import { useBoundedReads } from "../../composables/useBoundedReads";
import ImportPanel from "./ImportPanel.vue";
import SourcePolicyEditor from "./SourcePolicyEditor.vue";
import DependencyPanel from "./DependencyPanel.vue";
import PatchPanel from "./PatchPanel.vue";
import IndexJobsPanel from "./IndexJobsPanel.vue";
import JointPublicationPanel from "./JointPublicationPanel.vue";
import JointPublicationHistory from "./JointPublicationHistory.vue";
const jointHistory = ref<InstanceType<typeof JointPublicationHistory> | null>(
  null,
);
import ReleaseCompositionPanel from "./ReleaseCompositionPanel.vue";
import ReleasePeerPanel from "./ReleasePeerPanel.vue";
const dependencyPanel = ref<InstanceType<typeof DependencyPanel> | null>(null);
async function previewCandidateImpact(id: string) {
  await dependencyPanel.value?.previewCandidate(id);
}
defineExpose({ previewCandidateImpact, refreshAfterPrepared, afterNovelEdit });
import ReleaseSourcePicker from "./ReleaseSourcePicker.vue";
import ReleaseOriginSummary from "./ReleaseOriginSummary.vue";
import SemanticReviewPanel from "./SemanticReviewPanel.vue";
import type { ArtifactOrigin, OriginSelection } from "./releaseOrigins";
import {
  describeDependencies,
  dependencyNames,
  type DependencyPolicy,
} from "./dependencies";
import {
  defaultPolicy,
  describePolicy,
  type KnowledgePolicy,
} from "./knowledgePolicy";

interface Resource {
  dependencies?: DependencyPolicy;
  policy?: KnowledgePolicy;
  id: string;
  name: string;
  kind: string;
  head: string;
  text: string;
  revision: number;
}
interface Draft {
  policy?: KnowledgePolicy;
  revision: number;
  base_head: string;
  text: string;
}
interface Edit extends Draft {
  _origin?: OriginSelection;
  sourceOrigin?: ArtifactOrigin;
  sourceOperation?: {
    id: string;
    generation: string;
    base: string;
    policy: string;
    ipHead: string;
    frozenReason: string;
  };
  dependencies?: DependencyPolicy;
  useProspectiveInputs: boolean;
  frozen: boolean;
  frozenReason: string;
  policy: KnowledgePolicy;
  originalPolicy?: KnowledgePolicy;
  id: string;
  name: string;
  original: string;
  saved: boolean;
}
interface Update {
  origin?: OriginSelection;
  dependencies?: DependencyPolicy;
  frozen_change_reason?: string;
  policy?: KnowledgePolicy;
  resource_id: string;
  expected_head: string;
  text: string;
}
interface Release {
  retention?: { state: "EXPIRED"; expired_at: string };
  requires_semantic_review?: boolean;
  id: string;
  hash: string;
  status: string;
  manifest: {
    schema: number;
    expected_ip_head: string;
    ip_candidate_id: string;
    artifacts: {
      resource_id: string;
      expected_head: string;
      hash: string;
      origin?: ArtifactOrigin;
    }[];
  };
  contents?: Update[];
}
const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: Branch;
  candidates: Candidate[];
  blocked: boolean;
  initiallyOpenImports?: boolean;
  novelNavigation?: boolean;
}>();
const emit = defineEmits<{
  published: [];
  blocking: [boolean];
  expired: [];
  changePlan: [{ id: string; branch: string }];
  editResource: [request: ResourceNavigationRequest];
}>();
const root = `/projects/${props.project.id}`;
const novelReads = useBoundedReads({
  stopped: "返回共享资料的重查已停止，原输入保持原样。",
  timeout: "返回共享资料的重查超时，原输入保持原样，可以重试。",
});
let novelReadSequence = 0;
const resources = ref<Resource[]>([]);
const releases = ref<Release[]>([]);
const usableReleases = computed(() =>
  releases.value.filter((r) => !r.retention),
);
const edits = ref<Edit[]>([]);
const preview = ref<Release | null>(null);
const publishedText = ref<Record<string, string>>({});
const publishedPolicies = ref<Record<string, KnowledgePolicy | undefined>>({});
const publishedDependencies = ref<Record<string, DependencyPolicy | undefined>>(
  {},
);
const name = ref("");
const nameOperation = ref(newOperationID());
const releaseOperation = ref(newOperationID());
const selectedIP = ref("");
const busy = ref(false);
const saving = ref(false);
const importBlocking = ref(false);
const dependencyBlocking = ref(false);
const patchBlocking = ref(false);
const sourceBlocking = ref(false);
const semanticBlocking = ref(false);
const jointBlocking = ref(false);
const compositionBlocking = ref(false);
const peerBlocking = ref(false);
const semanticReady = ref(false);
const semanticKnownNegative = ref(false);
watch(
  () => preview.value?.id,
  () => {
    semanticReady.value = false;
    semanticKnownNegative.value = false;
  },
);
const semanticRequired = computed(
  () =>
    !!preview.value &&
    (semanticKnownNegative.value ||
      preview.value.requires_semantic_review ||
      !!preview.value.manifest.ip_candidate_id ||
      preview.value.manifest.artifacts.some((a) => a.origin?.review_required)),
);
const error = ref("");
const notice = ref("");
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const canAdopt = computed(
  () => props.actor.role !== "reader" && props.project.role !== "reader",
);
const canPublish = computed(
  () =>
    ["owner", "publisher"].includes(props.actor.role) &&
    ["owner", "publisher"].includes(props.project.role),
);
const unavailable = computed(
  () =>
    busy.value ||
    saving.value ||
    props.blocked ||
    importBlocking.value ||
    dependencyBlocking.value ||
    patchBlocking.value ||
    semanticBlocking.value ||
    sourceBlocking.value ||
    jointBlocking.value ||
    compositionBlocking.value ||
    peerBlocking.value,
);
const pending = computed(() => edits.value.some((e) => !e.saved));
let timer: ReturnType<typeof setTimeout> | undefined;
let disposed = false;
watch(name, () => {
  nameOperation.value = newOperationID();
});
watch(selectedIP, () => {
  releaseOperation.value = newOperationID();
});
watch(
  [
    busy,
    saving,
    pending,
    importBlocking,
    dependencyBlocking,
    patchBlocking,
    sourceBlocking,
    semanticBlocking,
    jointBlocking,
    compositionBlocking,
    peerBlocking,
  ],
  () =>
    emit(
      "blocking",
      busy.value ||
        saving.value ||
        pending.value ||
        importBlocking.value ||
        dependencyBlocking.value ||
        patchBlocking.value ||
        semanticBlocking.value ||
        sourceBlocking.value ||
        jointBlocking.value ||
        compositionBlocking.value ||
        peerBlocking.value,
    ),
  { immediate: true },
);
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function refreshAfterPrepared() {
  if (disposed) return;
  // Child emits before its own busy flag clears. Keep the old list disabled
  // until the newly saved candidate is actually present in the parent list.
  busy.value = true;
  try {
    await refresh();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function editNovelResource(request: ResourceNavigationRequest) {
  if (
    !props.novelNavigation ||
    !canEdit.value ||
    unavailable.value ||
    pending.value ||
    request.ref.kind !== "resource" ||
    request.ref.branch_id !== props.branch.id
  )
    return;
  emit("editResource", request);
}
async function afterNovelEdit() {
  await nextTick();
  if (!props.novelNavigation || disposed || unavailable.value || pending.value)
    return;
  const sequence = ++novelReadSequence;
  const projectID = props.project.id,
    branchID = props.branch.id,
    actorID = props.actor.id;
  const active = () =>
    !disposed &&
    novelReads.active() &&
    sequence === novelReadSequence &&
    props.project.id === projectID &&
    props.branch.id === branchID &&
    props.actor.id === actorID;
  await dependencyPanel.value?.afterNovelEdit();
  await nextTick();
  if (!active()) return;
  await perform(async () => {
    try {
      const [nextResources, nextReleases] = await Promise.all([
        novelReads.read<{ items: Resource[] }>(
          `${root}/resources?branch=${branchID}`,
        ),
        novelReads.read<{ items: Release[] }>(
          `${root}/releases?branch=${branchID}`,
        ),
      ]);
      if (!active()) return;
      resources.value = nextResources.items;
      releases.value = nextReleases.items;
      notice.value = "已重读共享资料列表；未重新生成候选或确认采用。";
    } catch (e) {
      if (!active()) return;
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        resources.value = [];
        releases.value = [];
        edits.value = [];
        preview.value = null;
        publishedText.value = {};
        publishedPolicies.value = {};
        publishedDependencies.value = {};
      }
      throw e;
    }
  });
}
async function perform(work: () => Promise<void>) {
  if (unavailable.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    fail(e);
  } finally {
    busy.value = false;
  }
}
async function refresh() {
  const [r, h] = await Promise.all([
    api<{ items: Resource[] }>(`${root}/resources?branch=${props.branch.id}`),
    api<{ items: Release[] }>(`${root}/releases?branch=${props.branch.id}`),
  ]);
  resources.value = r.items;
  releases.value = h.items;
}
async function create() {
  await perform(async () => {
    await api<Resource>(`${root}/resources`, "POST", {
      branch_id: props.branch.id,
      name: name.value,
      kind: "note",
      operation_id: nameOperation.value,
    });
    name.value = "";
    await refresh();
  });
}
async function edit(resource: Resource) {
  if (edits.value.some((e) => e.id === resource.id)) return;
  await perform(async () => {
    const [formal, draft] = await Promise.all([
      api<Resource>(`${root}/resources/${resource.id}`),
      api<Draft>(`${root}/resources/${resource.id}/draft`),
    ]);
    edits.value.push({
      ...sourceForResource(formal),
      dependencies: formal.dependencies,
      useProspectiveInputs: false,
      frozen: formal.dependencies?.frozen ?? false,
      frozenReason: "",
      id: resource.id,
      name: resource.name,
      original: formal.text,
      originalPolicy: formal.policy,
      policy: draft.policy ?? formal.policy ?? defaultPolicy(),
      revision: draft.revision,
      base_head: draft.revision ? draft.base_head : formal.head,
      text: draft.revision ? draft.text : formal.text,
      saved: true,
    });
    releaseOperation.value = newOperationID();
  });
}
function sourceForResource(resource: Resource): {
  _origin?: OriginSelection;
  sourceOrigin?: ArtifactOrigin;
} {
  for (const release of usableReleases.value) {
    const ref = release.manifest.artifacts.find(
      (a) => a.resource_id === resource.id,
    );
    if (!ref) continue;
    if (
      release.status !== "PUBLISHED" &&
      ref.expected_head === resource.head &&
      ref.origin?.actor_id === props.actor.id &&
      ref.origin.generation
    ) {
      return {
        _origin: { parent_release_id: release.id },
        sourceOrigin: ref.origin,
      };
    }
  }
  const published = usableReleases.value.find((r) => r.id === resource.head);
  return {
    _origin: resource.head ? { parent_release_id: resource.head } : undefined,
    sourceOrigin: published?.manifest.artifacts.find(
      (a) => a.resource_id === resource.id,
    )?.origin,
  };
}
async function selectGenerationSource(
  edit: Edit,
  source: { id: string; text: string },
) {
  if (
    edit.text !== source.text &&
    edit.text.trim() &&
    !window.confirm(
      "将所选生成结果载入编辑区？当前私人草稿会被替换，正式内容保持原版本。",
    )
  )
    return;
  await perform(async () => {
    const policy = JSON.stringify(edit.policy);
    if (
      !edit.sourceOperation ||
      edit.sourceOperation.generation !== source.id ||
      edit.sourceOperation.base !== edit.base_head ||
      edit.sourceOperation.policy !== policy ||
      edit.sourceOperation.ipHead !== props.branch.head ||
      edit.sourceOperation.frozenReason !== edit.frozenReason
    ) {
      edit.sourceOperation = {
        id: crypto.randomUUID(),
        generation: source.id,
        base: edit.base_head,
        policy,
        ipHead: props.branch.head,
        frozenReason: edit.frozenReason,
      };
    }
    // Persist provenance before replacing the private draft. A reload or lost
    // draft save cannot convert this known generation into a manual origin.
    const release = await api<Release>(`${root}/releases`, "POST", {
      branch_id: props.branch.id,
      expected_ip_head: props.branch.head,
      operation_id: edit.sourceOperation.id,
      updates: [
        {
          resource_id: edit.id,
          expected_head: edit.base_head,
          text: source.text,
          policy: edit.policy,
          frozen_change_reason: edit.frozenReason,
          origin: { generation_id: source.id },
        },
      ],
    });
    if (disposed) return;
    edit._origin = { parent_release_id: release.id };
    edit.sourceOrigin = release.manifest.artifacts.find(
      (a) => a.resource_id === edit.id,
    )?.origin;
    edit.text = source.text;
    changed(edit);
    await refresh();
    notice.value =
      "生成来源候选已保存并载入编辑区；修改会形成新候选，不继承原内容资格。";
  });
}
function changed(edit: Edit) {
  edit.saved = false;
  releaseOperation.value = newOperationID();
  scheduleSave();
}
async function resetToPublished(edit: Edit) {
  await perform(async () => {
    const current = await api<Resource>(`${root}/resources/${edit.id}`);
    edit.base_head = current.head;
    edit.text = current.text;
    edit.original = current.text;
    edit.policy = current.policy ?? defaultPolicy();
    edit.originalPolicy = current.policy;
    edit.dependencies = current.dependencies;
    edit.frozen = current.dependencies?.frozen ?? false;
    edit.frozenReason = "";
    edit.useProspectiveInputs = false;
    Object.assign(edit, sourceForResource(current));
    changed(edit);
    notice.value = "已明确选择以当前正式内容替换这份私人草稿，正在暂存。";
  });
}
function changeReason() {
  releaseOperation.value = newOperationID();
}
function scheduleSave() {
  clearTimeout(timer);
  timer = setTimeout(() => {
    void saveDrafts();
  }, 600);
}
async function saveDrafts() {
  if (disposed || !pending.value) return;
  if (unavailable.value) {
    scheduleSave();
    return;
  }
  saving.value = true;
  try {
    for (const edit of edits.value.filter((e) => !e.saved)) {
      const text = edit.text;
      const base = edit.base_head;
      const policy = JSON.parse(JSON.stringify(edit.policy)) as KnowledgePolicy;
      const saved = await api<Draft>(
        `${root}/resources/${edit.id}/draft`,
        "PUT",
        { revision: edit.revision, base_head: base, text, policy },
      );
      if (disposed) return;
      edit.revision = saved.revision;
      edit.saved =
        edit.text === text &&
        edit.base_head === base &&
        JSON.stringify(edit.policy) === JSON.stringify(policy);
    }
    if (pending.value) scheduleSave();
  } catch (e) {
    fail(e);
  } finally {
    saving.value = false;
  }
}
async function prepare() {
  await perform(async () => {
    await api<Release>(`${root}/releases`, "POST", {
      branch_id: props.branch.id,
      expected_ip_head: props.branch.head,
      ip_candidate_id: selectedIP.value,
      operation_id: releaseOperation.value,
      updates: edits.value.map((e) => ({
        resource_id: e.id,
        origin: e._origin,
        expected_head: e.base_head,
        text: e.text,
        policy: e.policy,
        frozen_change_reason: e.frozenReason,
        dependencies: prospectivePolicy(e),
      })),
    });
    await refresh();
    notice.value = "联合候选已保存。请展开对照检查，再确认整组内容。";
  });
}
function prospectivePolicy(edit: Edit): DependencyPolicy | undefined {
  if (!edit.useProspectiveInputs || !edit.dependencies) return undefined;
  const result: DependencyPolicy = JSON.parse(
    JSON.stringify(edit.dependencies),
  );
  for (const d of result.inputs) {
    if (d.pinned || d.source.branch_id !== props.branch.id) continue;
    if (
      d.source.kind === "resource" &&
      edits.value.some((e) => e.id === d.source.id)
    ) {
      d.source.revision = "$release";
      d.source.hash = "";
    } else if (
      ["entity", "assertion", "rule", "event", "mapping"].includes(
        d.source.kind,
      ) &&
      selectedIP.value
    ) {
      d.source.revision = "$candidate";
      d.source.hash = "";
    }
  }
  return result;
}
async function inspect(release: Release) {
  if (release.retention) return;
  await perform(async () => {
    preview.value = await api<Release>(`${root}/releases/${release.id}`);
    const current = await Promise.all(
      preview.value.manifest.artifacts.map((a) =>
        api<Resource>(`${root}/resources/${a.resource_id}`),
      ),
    );
    publishedText.value = Object.fromEntries(
      current.map((r) => [r.id, r.text]),
    );
    publishedPolicies.value = Object.fromEntries(
      current.map((r) => [r.id, r.policy]),
    );
    publishedDependencies.value = Object.fromEntries(
      current.map((r) => [r.id, r.dependencies]),
    );
  });
}
async function advance(action: "adopt" | "publish") {
  if (!preview.value) return;
  const before = preview.value;
  await perform(async () => {
    if (action === "publish")
      notice.value = "正在校验并准备整组内容的检索索引，完成后发布。";
    const result = await api<Release>(
      `${root}/releases/${before.id}/${action}`,
      "POST",
      {},
    );
    preview.value = {
      ...result,
      contents: before.contents,
      requires_semantic_review: before.requires_semantic_review,
    };
    if (action === "publish") {
      for (const update of before.contents ?? []) {
        const edit = edits.value.find((e) => e.id === update.resource_id);
        if (
          edit &&
          edit.text === update.text &&
          JSON.stringify(edit.policy) === JSON.stringify(update.policy)
        ) {
          edit.base_head = result.id;
          edit.original = update.text;
          edit.originalPolicy = update.policy;
          edit.dependencies = update.dependencies;
          edit.frozen = update.dependencies?.frozen ?? false;
          edit.frozenReason = "";
          edit.useProspectiveInputs = false;
          edit._origin = { parent_release_id: result.id };
          edit.sourceOrigin = result.manifest.artifacts.find(
            (a) => a.resource_id === edit.id,
          )?.origin;
          edit.saved = false;
        }
      }
      selectedIP.value = "";
      scheduleSave();
      emit("published");
      notice.value = "整组内容已发布，关联设定与资料保持同一版本。";
    } else {
      notice.value = "整组内容已确认采用，等待发布。";
    }
    await refresh();
  });
}
function guardUnload(event: BeforeUnloadEvent) {
  if (pending.value || saving.value) {
    event.preventDefault();
    event.returnValue = "";
  }
}
onMounted(() => {
  window.addEventListener("beforeunload", guardUnload);
  busy.value = true;
  void refresh()
    .catch(fail)
    .finally(() => {
      busy.value = false;
    });
});
onUnmounted(() => {
  disposed = true;
  clearTimeout(timer);
  window.removeEventListener("beforeunload", guardUnload);
  emit("blocking", false);
});
</script>

<template>
  <section class="panel bundle-panel">
    <h2>共享资料与联合发布</h2>
    <ReleasePeerPanel
      :project="project"
      :actor="actor"
      :branch="branch"
      :releases="usableReleases"
      :resources="resources"
      :blocked="
        busy ||
        saving ||
        blocked ||
        pending ||
        importBlocking ||
        dependencyBlocking ||
        patchBlocking ||
        sourceBlocking ||
        semanticBlocking ||
        jointBlocking ||
        compositionBlocking
      "
      @blocking="peerBlocking = $event"
      @expired="emit('expired')"
      @prepared="refreshAfterPrepared"
    />
    <ReleaseCompositionPanel
      :project="project"
      :actor="actor"
      :branch="branch"
      :releases="usableReleases"
      :resources="resources"
      :blocked="
        busy ||
        saving ||
        blocked ||
        pending ||
        importBlocking ||
        dependencyBlocking ||
        patchBlocking ||
        sourceBlocking ||
        semanticBlocking ||
        jointBlocking ||
        peerBlocking
      "
      @blocking="compositionBlocking = $event"
      @expired="emit('expired')"
      @prepared="refreshAfterPrepared"
    />
    <JointPublicationPanel
      :project="project"
      :actor="actor"
      :blocked="
        busy ||
        saving ||
        blocked ||
        pending ||
        importBlocking ||
        dependencyBlocking ||
        patchBlocking ||
        sourceBlocking ||
        semanticBlocking ||
        compositionBlocking ||
        peerBlocking
      "
      @blocking="jointBlocking = $event"
      @expired="emit('expired')"
      @published="
        emit('published');
        refreshAfterPrepared();
        jointHistory?.refresh();
      "
    />
    <JointPublicationHistory
      ref="jointHistory"
      :project="project"
      @expired="emit('expired')"
    />
    <DependencyPanel
      ref="dependencyPanel"
      :project="project"
      :actor="actor"
      :branch="branch"
      :resources="resources"
      :releases="usableReleases"
      :candidates="candidates"
      :novel-navigation="novelNavigation"
      :blocked="
        busy ||
        saving ||
        blocked ||
        pending ||
        importBlocking ||
        patchBlocking ||
        jointBlocking ||
        compositionBlocking ||
        peerBlocking
      "
      @prepared="refreshAfterPrepared"
      @use="emit('changePlan', $event)"
      @edit-resource="editNovelResource"
      @blocking="dependencyBlocking = $event"
      @expired="emit('expired')"
    />
    <PatchPanel
      :project="project"
      :actor="actor"
      :branch="branch"
      :resources="resources"
      :releases="usableReleases"
      :blocked="
        busy ||
        saving ||
        blocked ||
        pending ||
        importBlocking ||
        dependencyBlocking ||
        sourceBlocking ||
        semanticBlocking ||
        jointBlocking ||
        compositionBlocking ||
        peerBlocking
      "
      @prepared="refreshAfterPrepared"
      @blocking="patchBlocking = $event"
      @expired="emit('expired')"
    />
    <ImportPanel
      :initially-open="initiallyOpenImports"
      :project="project"
      :actor="actor"
      :branch="branch"
      :blocked="
        busy ||
        saving ||
        blocked ||
        pending ||
        dependencyBlocking ||
        patchBlocking ||
        jointBlocking ||
        compositionBlocking ||
        peerBlocking
      "
      @prepared="refreshAfterPrepared"
      @blocking="importBlocking = $event"
      @expired="emit('expired')"
    />
    <p class="muted">
      多份故事备忘、研究记录可作为一组检查和发布。若需要同步修改设定，可关联一个已确认采用的设定候选。
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <form v-if="canEdit" class="create" @submit.prevent="create">
      <label
        >资料名称<input
          v-model="name"
          required
          maxlength="160"
          :disabled="unavailable" /></label
      ><button :disabled="unavailable">创建资料</button>
    </form>
    <ul class="project-list">
      <li v-for="resource in resources" :key="resource.id">
        <button
          class="project-card"
          :disabled="unavailable"
          @click="edit(resource)"
        >
          {{ canEdit ? "编辑" : "查看" }} {{ resource.name }}</button
        ><small class="muted">{{
          resource.head ? `已发布 · ${resource.head.slice(0, 8)}` : "尚未发布"
        }}</small>
      </li>
    </ul>
    <div v-for="edit in edits" :key="edit.id" class="resource-edit">
      <h3>{{ edit.name }}</h3>
      <ReleaseSourcePicker
        v-if="canEdit"
        :project="project.id"
        :branch="branch.id"
        :disabled="unavailable || pending"
        @select="selectGenerationSource(edit, $event)"
        @blocking="sourceBlocking = $event"
        @expired="emit('expired')"
      />
      <p v-if="edit.sourceOrigin?.recovery?.length" class="notice">
        此资料从旧作品恢复，保留历史来源。旧生成任务不会重新执行；AI
        生成或原始来源未记录的内容，需要重新独立复核后才能采用。
      </p>
      <p v-if="edit.sourceOrigin?.generation" class="notice">
        此资料已记录生成来源
        {{
          edit.sourceOrigin.generation.generation_id
        }}。手改后旧资格仅对应原内容；保存新候选后检查当前内容是否需要独立复核。
      </p>
      <label v-if="edit.dependencies?.inputs.length" class="dependency-choice"
        ><input
          v-model="edit.useProspectiveInputs"
          type="checkbox"
          :disabled="unavailable || !canEdit"
          @change="changeReason"
        />将已记录的关联指向本组的新版本（固定历史版本的关联保持原样）</label
      >
      <SourcePolicyEditor
        :policy="edit.policy"
        :allow-withdrawal="
          resources.some(
            (r) => r.id === edit.id && ['note', 'source'].includes(r.kind),
          )
        "
        :entities="branch.snapshot.entities"
        :disabled="!canEdit || busy || dependencyBlocking"
        @changed="changed(edit)"
      />
      <label
        >{{ edit.name }} · 待保存内容<textarea
          v-model="edit.text"
          rows="7"
          :readonly="!canEdit"
          :disabled="busy || dependencyBlocking"
          @input="changed(edit)"
        ></textarea>
      </label>
      <label v-if="edit.frozen"
        >冻结资料修订说明（随候选保存）<textarea
          v-model="edit.frozenReason"
          :disabled="unavailable || !canEdit"
          maxlength="2000"
          @input="changeReason"
        />
      </label>
      <p class="muted">
        {{
          edit.saved ? "草稿已暂存；发布后才成为正式资料" : "正在暂存资料草稿…"
        }}
      </p>
      <p
        v-if="resources.find((r) => r.id === edit.id)?.head !== edit.base_head"
        class="error"
      >
        正式资料已变化。保留你的草稿，请对照最新版本处理后再创建联合候选。
      </p>
      <button
        v-if="
          resources.find((r) => r.id === edit.id)?.head !== edit.base_head &&
          canEdit
        "
        class="quiet"
        :disabled="unavailable || !edit.saved"
        @click="resetToPublished(edit)"
      >
        用当前正式内容替换这份私人草稿
      </button>
      <details>
        <summary>查看编辑时的正式内容</summary>
        <p>{{ describePolicy(edit.originalPolicy) }}</p>
        <pre class="prose">{{ edit.original || "尚无正式内容" }}</pre>
      </details>
      <button
        class="quiet"
        :disabled="unavailable || !edit.saved"
        @click="edits = edits.filter((e) => e.id !== edit.id)"
      >
        收起此资料
      </button>
    </div>
    <div v-if="canEdit && edits.length" class="bundle-actions">
      <label
        >同步发布设定<select v-model="selectedIP" :disabled="unavailable">
          <option value="">仅发布资料，引用当前正式设定</option>
          <option
            v-for="candidate in candidates.filter(
              (c) => c.status === 'READY' && c.base_head === branch.head,
            )"
            :key="candidate.id"
            :value="candidate.id"
          >
            采用的设定候选 {{ candidate.id.slice(0, 8) }}
          </option>
        </select></label
      >
      <div class="actions">
        <button :disabled="unavailable || pending" @click="prepare">
          保存联合候选</button
        ><button
          v-if="pending && error"
          class="quiet"
          :disabled="unavailable"
          @click="saveDrafts"
        >
          重试资料暂存
        </button>
      </div>
    </div>
    <h3>最近的联合版本</h3>
    <article
      v-for="release in releases"
      :key="release.id"
      class="candidate-card"
    >
      <p>
        {{
          release.status === "PUBLISHED"
            ? "已发布"
            : release.status === "READY"
              ? "已采用，待发布"
              : "待检查"
        }}
        · {{ release.manifest.artifacts.length }} 份资料 ·
        {{ release.id.slice(0, 8) }}
      </p>
      <p v-if="release.retention" class="notice">
        此版本的材料已过保存期限，保留历史记录，不能再次采用或发布。
      </p>
      <button
        class="quiet"
        :disabled="unavailable || !!release.retention"
        @click="inspect(release)"
      >
        检查联合候选
      </button>
    </article>
    <section
      v-if="preview"
      data-testid="release-preview"
      class="release-preview"
    >
      <h3>整组内容对照</h3>
      <p v-if="preview.manifest.schema !== 2" class="notice">
        旧清单仅供查看，请基于当前资料保存带来源的新候选。
      </p>
      <IndexJobsPanel
        v-if="preview.status === 'READY'"
        :key="preview.id"
        :project="project"
        :actor="actor"
        :branch="branch"
        :release-id="preview.id"
        :blocked="unavailable"
        label="后台准备这组内容的发布索引"
        @expired="emit('expired')"
      />
      <p v-if="preview.manifest.ip_candidate_id" class="notice">
        本次会同时发布已确认的设定候选
        {{ preview.manifest.ip_candidate_id.slice(0, 8) }}。
      </p>
      <article v-for="update in preview.contents" :key="update.resource_id">
        <h4>{{ resources.find((r) => r.id === update.resource_id)?.name }}</h4>
        <ReleaseOriginSummary
          :origin="
            preview.manifest.artifacts.find(
              (a) => a.resource_id === update.resource_id,
            )?.origin
          "
          :legacy="preview.manifest.schema !== 2"
        />
        <div class="field-grid">
          <div>
            <p class="eyebrow">当前正式内容</p>
            <p>{{ describePolicy(publishedPolicies[update.resource_id]) }}</p>
            <p>
              {{
                describeDependencies(publishedDependencies[update.resource_id])
              }}
            </p>
            <p
              v-for="d in publishedDependencies[update.resource_id]?.inputs"
              :key="d.source.id + d.kind"
            >
              {{ dependencyNames[d.kind] }} · {{ d.source.id.slice(0, 12) }} ·
              {{ d.reason }} · {{ d.pinned ? "固定版本" : "跟随最新" }} ·
              {{ d.source.revision.slice(0, 12) }}
            </p>
            <pre class="prose">{{
              publishedText[update.resource_id] || "尚无正式内容"
            }}</pre>
          </div>
          <div>
            <p class="eyebrow">本次候选</p>
            <p>{{ describePolicy(update.policy) }}</p>
            <p>{{ describeDependencies(update.dependencies) }}</p>
            <p
              v-for="d in update.dependencies?.inputs"
              :key="d.source.id + d.kind"
            >
              {{ dependencyNames[d.kind] }} · {{ d.source.id.slice(0, 12) }} ·
              {{ d.reason }} · {{ d.pinned ? "固定版本" : "跟随最新" }} ·
              {{ d.source.revision.slice(0, 12) }}
            </p>
            <p v-if="update.frozen_change_reason" class="notice">
              冻结修订原因：{{ update.frozen_change_reason }}；须由所有者确认。
            </p>
            <pre class="prose">{{ update.text }}</pre>
          </div>
        </div>
      </article>
      <SemanticReviewPanel
        v-if="preview.manifest.schema === 2 && preview.status !== 'PUBLISHED'"
        :key="preview.id"
        :project="project"
        :actor="actor"
        :branch="branch.id"
        target-kind="artifact_release"
        :target-i-d="preview.id"
        :target-hash="preview.hash"
        :required="semanticRequired"
        :blocked="busy || saving || props.blocked"
        @ready="semanticReady = $event"
        @review-required="semanticKnownNegative ||= $event"
        @blocking="semanticBlocking = $event"
        @expired="emit('expired')"
      />
      <div class="actions">
        <button
          v-if="canAdopt && preview.status === 'PREPARED'"
          :disabled="
            unavailable ||
            preview.manifest.schema !== 2 ||
            (semanticRequired && !semanticReady)
          "
          @click="advance('adopt')"
        >
          确认采用整组内容</button
        ><button
          v-if="canPublish && preview.status === 'READY'"
          :disabled="
            unavailable ||
            preview.manifest.schema !== 2 ||
            (semanticRequired && !semanticReady)
          "
          @click="advance('publish')"
        >
          发布整组内容
        </button>
      </div>
    </section>
  </section>
</template>
<style scoped>
.dependency-choice {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  margin: 0.7rem 0;
}
.dependency-choice input {
  width: auto;
}
</style>
