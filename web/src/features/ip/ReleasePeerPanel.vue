<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import {
  APIError,
  api,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";
import type { DependencyEntry, DependencyPolicy } from "./dependencies";
import type { Snapshot } from "./types";

interface Choice {
  id: string;
  hash: string;
  status: string;
  retention?: unknown;
  manifest: { expected_ip_head: string };
}
interface Content {
  resource_id: string;
  expected_head: string;
  text: string;
  dependencies?: DependencyPolicy;
  origin?: { parent_release_id: string };
  [key: string]: unknown;
}
interface FullRelease extends Choice {
  manifest: {
    expected_ip_head: string;
    branch_id: string;
    ip_candidate_id: string;
    peer_releases?: string[];
  };
  contents: Content[];
}
interface Peer {
  release: FullRelease;
  entries: DependencyEntry[];
  snapshot: Snapshot;
}
const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: { id: string; head: string };
  releases: Choice[];
  resources: { id: string; name: string }[];
  blocked: boolean;
}>();
const emit = defineEmits<{
  blocking: [boolean];
  expired: [];
  prepared: [string];
}>();
const root = `/projects/${props.project.id}`;
const busy = ref(false),
  error = ref(""),
  notice = ref("");
const targetId = ref("");
const target = ref<FullRelease>();
const updates = ref<Content[]>([]);
const branches = ref<{ id: string; name: string }[]>([]);
const sourceBranch = ref("");
const choices = ref<Choice[]>([]);
const sourceId = ref("");
const peers = ref<Peer[]>([]);
const chosen = ref<string[]>([]);
const reviewed = ref(false);
let disposed = false;
let request: Record<string, unknown> | undefined;
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const targets = computed(() =>
  props.releases.filter(
    (r) =>
      r.status !== "PUBLISHED" &&
      !r.retention &&
      r.manifest.expected_ip_head === props.branch.head,
  ),
);
const bindings = computed(() =>
  updates.value.flatMap((update) =>
    (update.dependencies?.inputs ?? []).flatMap((dep, index) => {
      if (dep.pinned) return [];
      const peer = peers.value.find((p) =>
        p.entries.some(
          ({ ref }) =>
            ref.kind === dep.source.kind &&
            ref.branch_id === dep.source.branch_id &&
            ref.id === dep.source.id &&
            ref.revision !== dep.source.revision,
        ),
      );
      const entry = peer?.entries.find(
        ({ ref }) =>
          ref.kind === dep.source.kind &&
          ref.branch_id === dep.source.branch_id &&
          ref.id === dep.source.id,
      );
      return peer && entry
        ? [
            {
              key: `${update.resource_id}:${index}`,
              resource: update.resource_id,
              index,
              before: dep.source,
              after: entry.ref,
              name: entry.name,
              branch: entry.branch_name,
              peer: peer.release.id,
            },
          ]
        : [];
    }),
  ),
);
const changed = computed(() =>
  bindings.value.filter((item) => chosen.value.includes(item.key)),
);
const pending = computed(
  () => busy.value || !!targetId.value || peers.value.length > 0,
);
watch(pending, (value) => emit("blocking", value), { immediate: true });
watch(
  [updates, peers, chosen],
  () => {
    request = undefined;
    reviewed.value = false;
  },
  { deep: true },
);
onUnmounted(() => {
  disposed = true;
  emit("blocking", false);
});
function name(id: string) {
  return props.resources.find((resource) => resource.id === id)?.name || id;
}
async function perform(action: () => Promise<void>) {
  if (busy.value || props.blocked || !canEdit.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await action();
  } catch (e) {
    if (e instanceof APIError && e.status === 401) emit("expired");
    if (!disposed) error.value = message(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function open(event: Event) {
  if (!(event.target as HTMLDetailsElement).open || branches.value.length)
    return;
  await perform(loadBranches);
}
async function loadBranches() {
  const result = await api<{ items: { id: string; name: string }[] }>(
    `${root}/branches`,
  );
  if (disposed) return;
  branches.value = result.items.filter((b) => b.id !== props.branch.id);
  sourceBranch.value = branches.value[0]?.id ?? "";
}
async function loadTarget() {
  await perform(async () => {
    // A rapid selection can run before the browser dispatches <details> toggle.
    // Load required branch choices inside this action instead of losing the load.
    if (!branches.value.length) await loadBranches();
    target.value = undefined;
    updates.value = [];
    chosen.value = [];
    request = undefined;
    if (!targetId.value) return;
    const result = await api<FullRelease>(`${root}/releases/${targetId.value}`);
    if (disposed) return;
    target.value = result;
    updates.value = structuredClone(result.contents).map((update) => ({
      ...update,
      origin: { parent_release_id: result.id },
    }));
  });
}
async function loadSources() {
  await perform(async () => {
    choices.value = [];
    sourceId.value = "";
    if (!sourceBranch.value) return;
    const result = await api<{ items: Choice[] }>(
      `${root}/releases?branch=${encodeURIComponent(sourceBranch.value)}`,
    );
    if (disposed) return;
    choices.value = result.items.filter(
      (r) => r.status !== "PUBLISHED" && !r.retention,
    );
    sourceId.value = choices.value[0]?.id ?? "";
  });
}
async function addPeer() {
  await perform(async () => {
    if (!sourceId.value) return;
    const result = await api<Peer>(
      `${root}/releases/${sourceId.value}/peer-sources`,
    );
    if (disposed) return;
    if (
      peers.value.some(
        (p) =>
          p.release.manifest.branch_id === result.release.manifest.branch_id,
      )
    )
      throw new Error("同一上游分支只能选择一个候选版本，请先移除旧选择。");
    if (
      result.release.manifest.branch_id === props.branch.id ||
      result.release.status === "PUBLISHED"
    )
      throw new Error("请选择其他分支尚未发布的候选。");
    peers.value.push(result);
  });
}
function reset() {
  targetId.value = "";
  target.value = undefined;
  updates.value = [];
  peers.value = [];
  chosen.value = [];
  reviewed.value = false;
  request = undefined;
}
async function save() {
  if (!target.value || !reviewed.value || changed.value.length === 0) return;
  await perform(async () => {
    if (!request) {
      const changedUpdates: Content[] = JSON.parse(
        JSON.stringify(updates.value),
      );
      for (const binding of changed.value) {
        const dependency = changedUpdates.find(
          (u) => u.resource_id === binding.resource,
        )?.dependencies?.inputs[binding.index];
        if (!dependency) throw new Error("引用发生变化，请重新预览。");
        dependency.source = binding.after;
      }
      request = {
        operation_id: newOperationID(),
        branch_id: props.branch.id,
        expected_ip_head: target.value!.manifest.expected_ip_head,
        ip_candidate_id: target.value!.manifest.ip_candidate_id,
        updates: changedUpdates,
        peer_releases: peers.value
          .filter((p) =>
            changed.value.some((binding) => binding.peer === p.release.id),
          )
          .map((p) => ({
            release_id: p.release.id,
            manifest_hash: p.release.hash,
          }))
          .sort((a, b) => a.release_id.localeCompare(b.release_id)),
      };
    }
    const result = await api<{ id: string }>(
      `${root}/releases`,
      "POST",
      request,
    );
    if (disposed) return;
    reset();
    notice.value =
      "已保存引用上游新版本的独立候选。请完成独立复核；上游未发布时须一起发布。";
    emit("prepared", result.id);
  });
}
</script>

<template>
  <details v-if="canEdit" class="release-peer-panel" @toggle="open">
    <summary>引用其他分支的新候选</summary>
    <p class="muted">
      先选择本分支候选，再选择上游新版本。原候选保留；固定历史的引用不会自动改绑。保存不会调用模型或发布作品。
    </p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <fieldset :disabled="busy || blocked">
      <legend>选择需要协同的版本</legend>
      <label
        >本分支候选<select
          v-model="targetId"
          aria-label="本分支候选"
          @change="loadTarget"
        >
          <option value="">请选择</option>
          <option
            v-for="candidate in targets"
            :key="candidate.id"
            :value="candidate.id"
          >
            {{ candidate.id }} ·
            {{ candidate.status === "READY" ? "已采用" : "待复核" }}
          </option>
        </select></label
      >
      <div class="peer-source-controls">
        <label
          >上游分支<select
            aria-label="上游分支"
            v-model="sourceBranch"
            @change="
              choices = [];
              sourceId = '';
            "
          >
            <option value="">请选择</option>
            <option
              v-for="branch in branches"
              :key="branch.id"
              :value="branch.id"
            >
              {{ branch.name }}
            </option>
          </select></label
        ><button type="button" :disabled="!sourceBranch" @click="loadSources">
          加载上游候选
        </button>
      </div>
      <label
        >上游候选<select v-model="sourceId" aria-label="上游候选">
          <option value="">请选择</option>
          <option
            v-for="candidate in choices"
            :key="candidate.id"
            :value="candidate.id"
          >
            {{ candidate.id }} ·
            {{ candidate.status === "READY" ? "已采用" : "待复核" }}
          </option>
        </select></label
      >
      <button
        type="button"
        :disabled="!sourceId || peers.length >= 15"
        @click="addPeer"
      >
        加入上游预览
      </button>
      <article v-for="peer in peers" :key="peer.release.id" class="peer-source">
        <h4>
          {{
            branches.find(
              (branch) => branch.id === peer.release.manifest.branch_id,
            )?.name
          }}
          · {{ peer.release.id }}
        </h4>
        <button
          type="button"
          @click="peers = peers.filter((p) => p.release.id !== peer.release.id)"
        >
          移除此上游版本
        </button>
        <details
          v-for="content in peer.release.contents"
          :key="content.resource_id"
          open
        >
          <summary>
            {{
              peer.entries.find((entry) => entry.ref.id === content.resource_id)
                ?.name || content.resource_id
            }}
          </summary>
          <pre>{{ content.text }}</pre>
        </details>
        <details
          v-if="
            peer.snapshot.entities.length || peer.snapshot.assertions.length
          "
        >
          <summary>上游人物与设定</summary>
          <p v-for="entity in peer.snapshot.entities" :key="entity.id">
            {{ entity.name }}：{{ entity.description }}
          </p>
          <p v-for="assertion in peer.snapshot.assertions" :key="assertion.id">
            {{
              peer.snapshot.entities.find(
                (entity) => entity.id === assertion.subject,
              )?.name || assertion.subject
            }}
            · {{ assertion.predicate }}：{{ assertion.value }}（{{
              assertion.kind
            }}，故事位置 {{ assertion.effective_from }} 起）
          </p>
        </details>
      </article>
      <p v-if="target && peers.length && !bindings.length">
        没有可改绑的跟随引用。请先记录本分支资料与上游对象的依赖；固定历史引用保持原版本。
      </p>
      <label v-for="binding in bindings" :key="binding.key" class="peer-check"
        ><input v-model="chosen" type="checkbox" :value="binding.key" />{{
          name(binding.resource)
        }}
        引用 {{ binding.branch }} / {{ binding.name }}：{{
          binding.before.revision
        }}
        → {{ binding.after.revision }}</label
      >
      <details v-for="update in updates" :key="update.resource_id">
        <summary>比较并编辑：{{ name(update.resource_id) }}</summary>
        <div class="peer-comparison">
          <div>
            <strong>原候选</strong>
            <pre>{{
              target?.contents.find(
                (original) => original.resource_id === update.resource_id,
              )?.text
            }}</pre>
          </div>
          <label>新候选正文<textarea v-model="update.text" rows="8" /></label>
        </div>
      </details>
      <label class="peer-check"
        ><input
          v-model="reviewed"
          type="checkbox"
          :disabled="!changed.length"
        />已核对上游内容及上述引用变化，保存为新候选</label
      >
      <div class="peer-actions">
        <button
          type="button"
          :disabled="!target || !changed.length || !reviewed"
          @click="save"
        >
          保存协同候选</button
        ><button type="button" @click="reset">放弃当前选择</button>
      </div>
    </fieldset>
  </details>
</template>

<style scoped>
.release-peer-panel {
  margin-block: 1rem;
  border: 1px solid var(--line, #d9dfd4);
  padding: 0.8rem;
  border-radius: 12px;
}
fieldset {
  display: grid;
  gap: 0.8rem;
}
.peer-source-controls,
.peer-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: 0.7rem;
}
.peer-source-controls label {
  flex: 1;
}
.peer-source {
  border-top: 1px solid var(--line, #d9dfd4);
  padding-top: 0.7rem;
  overflow-wrap: anywhere;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: inherit;
  max-height: 22rem;
  overflow: auto;
  background: #f3f5ef;
  padding: 0.7rem;
}
.peer-comparison {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.8rem;
}
.peer-check {
  display: flex;
  align-items: start;
  gap: 0.5rem;
  overflow-wrap: anywhere;
}
.peer-check input {
  width: auto;
  flex: none;
  margin-top: 0.25rem;
}
@media (max-width: 700px) {
  .peer-comparison {
    grid-template-columns: 1fr;
  }
}
</style>
