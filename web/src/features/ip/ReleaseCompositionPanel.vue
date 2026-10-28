<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";
type Release = {
  id: string;
  hash: string;
  status: string;
  manifest: { expected_ip_head: string; artifacts: { resource_id: string }[] };
};
type Preview = {
  updates: { resource_id: string; text: string }[];
  conflicts: { resource_id: string; reason: string }[];
  rebindings: { resource_id: string; target: string }[];
};
const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: { id: string; head: string };
  releases: Release[];
  resources: { id: string; name: string; text: string }[];
  blocked: boolean;
}>();
const emit = defineEmits<{
  blocking: [boolean];
  expired: [];
  prepared: [string];
}>();
const chosen = ref<string[]>([]),
  rebind = ref(false),
  busy = ref(false),
  error = ref(""),
  notice = ref(""),
  preview = ref<Preview | null>(null);
const operation = ref(newOperationID());
const request = ref<{
  operation_id: string;
  releases: { release_id: string; manifest_hash: string }[];
  rebind_following: boolean;
} | null>(null);
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const choices = computed(() =>
  props.releases.filter(
    (r) =>
      r.status !== "PUBLISHED" &&
      r.manifest.expected_ip_head === props.branch.head,
  ),
);
let disposed = false;
function name(id: string) {
  return props.resources.find((r) => r.id === id)?.name || id;
}
watch(
  [chosen, rebind],
  () => {
    operation.value = newOperationID();
    request.value = null;
    preview.value = null;
  },
  { deep: true },
);
watch(
  [chosen, busy],
  () => emit("blocking", busy.value || chosen.value.length > 0),
  { deep: true },
);
async function perform(work: () => Promise<void>) {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    if (!disposed) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    busy.value = false;
  }
}
async function inspect() {
  await perform(async () => {
    if (!canEdit.value || chosen.value.length < 2) return;
    const pins = chosen.value
      .map((id) => {
        const r = props.releases.find((r) => r.id === id);
        if (!r) throw new Error("所选候选已不可用，请刷新。");
        return { release_id: r.id, manifest_hash: r.hash };
      })
      .sort((a, b) => a.release_id.localeCompare(b.release_id));
    request.value = {
      operation_id: operation.value,
      releases: pins,
      rebind_following: rebind.value,
    };
    preview.value = null;
    const result = await api<Preview>(
      `/projects/${props.project.id}/releases/composition/preview`,
      "POST",
      request.value,
    );
    if (!disposed) preview.value = result;
  });
}
async function save() {
  await perform(async () => {
    if (
      !canEdit.value ||
      !preview.value ||
      preview.value.conflicts.length ||
      !request.value
    )
      return;
    const result = await api<{ id: string }>(
      `/projects/${props.project.id}/releases/composition`,
      "POST",
      request.value,
    );
    if (disposed) return;
    chosen.value = [];
    preview.value = null;
    request.value = null;
    notice.value =
      "组合候选已保存。原候选保留；请独立复核这份新候选，再确认采用和发布。";
    emit("prepared", result.id);
  });
}
function guardUnload(e: BeforeUnloadEvent) {
  if (chosen.value.length || busy.value) {
    e.preventDefault();
    e.returnValue = "";
  }
}
window.addEventListener("beforeunload", guardUnload);
onUnmounted(() => {
  disposed = true;
  window.removeEventListener("beforeunload", guardUnload);
  emit("blocking", false);
});
</script>
<template>
  <details>
    <summary>合并本分支的修订批次</summary>
    <section role="group" aria-label="修订批次组合">
      <p>
        把同一基础版本上的多批修改组合成一份新候选。保留原始来源；不自动采用或发布，组合后需要新的独立复核。
      </p>
      <p v-if="error" role="alert">{{ error }}</p>
      <p v-if="notice" role="status">{{ notice }}</p>
      <fieldset :disabled="busy || blocked || !canEdit">
        <label v-for="r in choices" :key="r.id" class="composition-choice"
          ><input
            v-model="chosen"
            type="checkbox"
            :value="r.id"
            :disabled="chosen.length >= 16 && !chosen.includes(r.id)"
          />{{
            r.manifest.artifacts.map((a) => name(a.resource_id)).join("、")
          }}
          · {{ r.id }}</label
        >
        <p>
          已选
          {{
            chosen.length
          }}
          份候选；本次2—16份，共最多32份资料。同一资料有不同修改时先显示冲突，固定历史引用不改。
        </p>
        <label class="composition-choice"
          ><input
            v-model="rebind"
            type="checkbox"
          />将本组内跟随最新的引用改绑到组合版本</label
        >
        <button type="button" :disabled="chosen.length < 2" @click="inspect">
          预览组合与引用变化
        </button>
        <button type="button" :disabled="!chosen.length" @click="chosen = []">
          清空组合选择
        </button>
        <section v-if="preview">
          <p>
            {{
              preview.rebindings.length
            }}
            处引用将指向组合版本；其他历史引用保持。
          </p>
          <ul v-if="preview.conflicts.length">
            <li v-for="(c, i) in preview.conflicts" :key="i">
              {{ name(c.resource_id) }}：{{ c.reason }}
            </li>
          </ul>
          <ul>
            <li v-for="(change, i) in preview.rebindings" :key="i">
              {{ name(change.resource_id) }} →
              {{ name(change.target) }}的组合版本
            </li>
          </ul>
          <article v-for="u in preview.updates" :key="u.resource_id">
            <h4>{{ name(u.resource_id) }}</h4>
            <div class="composition-compare">
              <div>
                <strong>当前正式内容</strong>
                <pre>{{
                  resources.find((r) => r.id === u.resource_id)?.text ||
                  "尚无正式内容"
                }}</pre>
              </div>
              <div>
                <strong>组合后候选</strong>
                <pre>{{ u.text }}</pre>
              </div>
            </div>
          </article>
        </section>
        <button
          type="button"
          :disabled="!preview || !!preview.conflicts.length"
          @click="save"
        >
          保存组合候选，随后独立复核
        </button>
      </fieldset>
    </section>
  </details>
</template>
<style scoped>
.composition-choice {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.composition-choice input {
  width: auto;
  margin: 0;
}
.composition-compare {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
@media (max-width: 700px) {
  .composition-compare {
    grid-template-columns: 1fr;
  }
}
</style>
