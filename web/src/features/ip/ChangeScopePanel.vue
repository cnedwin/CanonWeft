<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { api, APIError, message, newOperationID } from "../../api";
import type { DependencyEntry, DependencyRef } from "./dependencies";

const props = defineProps<{
  projectId: string;
  branchId: string;
  planId: string;
  stale: boolean;
  catalog: DependencyEntry[];
  included: DependencyRef[];
  blocked: boolean;
}>();
const emit = defineEmits<{
  expanded: [id: string];
  blocking: [value: boolean];
  expired: [];
}>();
type Hit = {
  id: string;
  resource_id: string;
  name: string;
  revision: string;
  text: string;
  start: number;
  end: number;
};
const branch = ref(props.branchId);
const query = ref("");
const storyTime = ref(0);
const manualID = ref("");
const hits = ref<Hit[]>([]);
const omissions = ref<string[]>([]);
const searched = ref(false);
const choices = ref<{ node: DependencyRef; name: string; reason: string }[]>(
  [],
);
const busy = ref(false);
const error = ref("");
const operation = ref(newOperationID());
const savedID = ref("");
let disposed = false;
const unavailable = computed(
  () => busy.value || props.blocked || props.stale || !!savedID.value,
);
const resources = computed(() =>
  props.catalog.filter((entry) => entry.ref.kind === "resource"),
);
const branches = computed(() => [
  ...new Map(
    resources.value.map((entry) => [entry.ref.branch_id, entry.branch_name]),
  ).entries(),
]);
watch(
  branches,
  (items) => {
    if (!items.some(([id]) => id === branch.value))
      branch.value = items[0]?.[0] ?? "";
  },
  { immediate: true },
);
const available = computed(() =>
  resources.value.filter(
    (entry) => entry.ref.branch_id === branch.value && !selected(entry.ref),
  ),
);
function selected(ref: DependencyRef) {
  return (
    props.included.some(
      (item) =>
        item.id === ref.id &&
        item.branch_id === ref.branch_id &&
        item.kind === ref.kind,
    ) || choices.value.some((item) => item.node.id === ref.id)
  );
}
function entryFor(hit: Hit) {
  return resources.value.find(
    (entry) =>
      entry.ref.id === hit.resource_id &&
      entry.ref.branch_id === branch.value &&
      entry.ref.revision === hit.revision,
  );
}
function add(entry?: DependencyEntry) {
  if (!entry || unavailable.value || selected(entry.ref)) return;
  choices.value.push({ node: { ...entry.ref }, name: entry.name, reason: "" });
  manualID.value = "";
}
watch(
  choices,
  () => {
    operation.value = newOperationID();
    emit("blocking", busy.value || choices.value.length > 0);
  },
  { deep: true, flush: "sync" },
);
watch(busy, (value) => emit("blocking", value || choices.value.length > 0));
watch(branch, () => {
  hits.value = [];
  omissions.value = [];
  searched.value = false;
  manualID.value = "";
});
async function search() {
  if (unavailable.value || !query.value.trim() || !branch.value) return;
  busy.value = true;
  error.value = "";
  hits.value = [];
  omissions.value = [];
  searched.value = false;
  try {
    const result = await api<{
      evidence?: Hit[];
      working_materials?: Hit[];
      omissions?: string[];
    }>(
      `/projects/${props.projectId}/branches/${branch.value}/knowledge/search`,
      "POST",
      {
        query: query.value.trim(),
        purpose: "critic",
        perspective: "",
        story_time: storyTime.value,
        limit: 20,
        token_budget: 65536,
        include_working: true,
      },
    );
    if (disposed) return;
    hits.value = [
      ...new Map(
        [...(result.evidence ?? []), ...(result.working_materials ?? [])].map(
          (hit) => [hit.id, hit],
        ),
      ).values(),
    ];
    omissions.value = result.omissions ?? [];
    searched.value = true;
  } catch (e) {
    if (!disposed) error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function save() {
  if (
    unavailable.value ||
    !choices.value.length ||
    choices.value.some((choice) => !choice.reason.trim())
  )
    return;
  busy.value = true;
  error.value = "";
  try {
    const result = await api<{ id: string }>(
      `/projects/${props.projectId}/changes/${props.planId}/scope`,
      "POST",
      {
        operation_id: operation.value,
        targets: choices.value.map(({ node, reason }) => ({ node, reason })),
      },
    );
    if (disposed) return;
    savedID.value = result.id;
    choices.value = [];
    busy.value = false;
    emit("blocking", false);
    emit("expanded", result.id);
  } catch (e) {
    if (!disposed) error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    if (!disposed) busy.value = false;
  }
}
onUnmounted(() => {
  disposed = true;
  emit("blocking", false);
});
</script>

<template>
  <section aria-label="补充检查范围" class="change-scope">
    <h4>查找可能漏掉的内容</h4>
    <p>
      检索线索或手动选择资料，填写理由后纳入新清单。补充对象仍待审查，不会自动登记依赖或修改正文。
    </p>
    <p v-if="stale" role="status">清单版本已变化，请先重新检查目标版本。</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <fieldset :disabled="unavailable">
      <label
        >查找资料的分支<select v-model="branch" aria-label="查找资料的分支">
          <option value="" disabled>选择分支</option>
          <option v-for="[id, name] in branches" :key="id" :value="id">
            {{ name }}
          </option>
        </select></label
      >
      <label
        >可能受影响的线索<input
          v-model="query"
          maxlength="1000"
          placeholder="角色别名、动作、道具、因果或场景描述"
      /></label>
      <label
        >检索故事位置<input v-model.number="storyTime" type="number" step="1"
      /></label>
      <button
        type="button"
        :disabled="!query.trim() || !Number.isSafeInteger(storyTime)"
        @click="search"
      >
        检索可能遗漏的内容
      </button>
      <p class="muted">
        使用当前分支的已建索引进行关键词与语义检索，最多返回20个片段；索引未就绪时可先到知识与证据区构建，也可手动选择。这里只收集候选线索，不是全书语义审查。
      </p>
      <p v-for="omission in omissions" :key="omission">{{ omission }}</p>
      <p v-if="searched && !hits.length">
        本次检索没有命中；不能据此判断没有影响。
      </p>
      <article v-for="hit in hits" :key="hit.id" class="scope-hit">
        <strong>{{ hit.name }}</strong>
        <p class="muted">
          版本 {{ hit.revision.slice(0, 8) }} · 原文位置 {{ hit.start }}—{{
            hit.end
          }}
        </p>
        <blockquote>{{ hit.text }}</blockquote>
        <button
          type="button"
          :disabled="!entryFor(hit) || selected(entryFor(hit)!.ref)"
          @click="add(entryFor(hit))"
        >
          {{
            entryFor(hit)
              ? selected(entryFor(hit)!.ref)
                ? "已在检查范围"
                : "选择这个检查对象"
              : "来源版本已变化，请刷新目录"
          }}
        </button>
      </article>
      <label
        >手动选择检查对象<select
          v-model="manualID"
          aria-label="手动选择检查对象"
        >
          <option value="">选择该分支资料</option>
          <option
            v-for="entry in available"
            :key="entry.ref.id"
            :value="entry.ref.id"
          >
            {{ entry.name }} · {{ entry.ref.id.slice(0, 8) }}
          </option>
        </select></label
      >
      <button
        type="button"
        :disabled="!manualID"
        @click="add(available.find((entry) => entry.ref.id === manualID))"
      >
        选择此资料
      </button>
      <div v-for="choice in choices" :key="choice.node.id" class="scope-choice">
        <strong>{{ choice.name }}</strong
        ><label
          >纳入理由 {{ choice.name
          }}<textarea v-model="choice.reason" maxlength="400" rows="2" /></label
        ><button
          type="button"
          @click="
            choices = choices.filter((item) => item.node.id !== choice.node.id)
          "
        >
          移出本次补充
        </button>
      </div>
      <button
        type="button"
        :disabled="
          !choices.length || choices.some((choice) => !choice.reason.trim())
        "
        @click="save"
      >
        保存补充检查清单
      </button>
      <button v-if="choices.length" type="button" @click="choices = []">
        放弃本次补充
      </button>
    </fieldset>
    <p v-if="savedID">
      新清单已保存，原清单保留。<button
        type="button"
        @click="emit('expanded', savedID)"
      >
        打开已保存的新清单
      </button>
    </p>
  </section>
</template>

<style scoped>
.change-scope {
  border: 1px solid #d5ddd3;
  border-radius: 10px;
  padding: 1rem;
  margin: 1rem 0;
}
.scope-hit,
.scope-choice {
  border-top: 1px solid #d5ddd3;
  padding: 0.8rem 0;
}
blockquote {
  margin: 0.7rem 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  padding-left: 1rem;
  border-left: 3px solid #8fa38c;
}
.muted {
  font-size: 0.85rem;
  color: #596959;
}
button {
  margin: 0.3rem 0.5rem 0.3rem 0;
}
</style>
