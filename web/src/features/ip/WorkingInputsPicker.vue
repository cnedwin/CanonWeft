<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, APIError, message } from "../../api";
interface WorkingRef {
  kind: string;
  id: string;
  version: string;
}
interface Choice {
  ref: WorkingRef;
  name: string;
  state: string;
}
const props = defineProps<{
  project: string;
  branch: string;
  blocked: boolean;
  revision: number;
}>();
const emit = defineEmits<{ change: [WorkingRef[]]; expired: [] }>();
const choices = ref<Choice[]>([]);
const selected = ref<Choice[]>([]);
const next = ref("");
const busy = ref(false);
const error = ref("");
let sequence = 0;
let disposed = false;
const key = (ref: WorkingRef) => `${ref.kind}:${ref.id}:${ref.version}`;
const chosen = (item: Choice) =>
  selected.value.some((value) => key(value.ref) === key(item.ref));
const labels: Record<string, string> = {
  resource_draft: "我的资料草稿",
  ip_draft: "我的设定草稿",
  ip_candidate: "设定候选",
  release_candidate: "资料候选",
};
function toggle(item: Choice) {
  if (props.blocked || busy.value) return;
  if (chosen(item))
    selected.value = selected.value.filter(
      (value) => key(value.ref) !== key(item.ref),
    );
  else if (selected.value.length < 16) selected.value.push(item);
  emit(
    "change",
    selected.value.map((value) => value.ref),
  );
}
async function load(more = false) {
  if (more && busy.value) return;
  const current = ++sequence;
  busy.value = true;
  error.value = "";
  try {
    const result = await api<{ items: Choice[]; next_cursor?: string }>(
      `/projects/${props.project}/branches/${props.branch}/knowledge/working-inputs?after=${encodeURIComponent(more ? next.value : "")}`,
    );
    if (disposed || current !== sequence) return;
    const combined = more ? [...choices.value, ...result.items] : result.items;
    choices.value = Array.from(
      new Map(combined.map((item) => [key(item.ref), item])).values(),
    );
    next.value = result.next_cursor ?? "";
  } catch (e) {
    if (!disposed && current === sequence) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (!disposed && current === sequence) busy.value = false;
  }
}
onMounted(() => load());
watch(
  () => props.revision,
  () => load(),
);
onBeforeUnmount(() => {
  disposed = true;
  sequence++;
});
</script>

<template>
  <fieldset class="working-picker">
    <legend>明确选用未确认资料</legend>
    <p class="muted">
      仅用于规划、审查和研究，最多 16
      项；私人草稿只列出你自己的。选中的是当前版本，刷新清单不会悄悄换成修改后的稿件。已修改、已发布或基础版本过期时，需要重新选择。
    </p>
    <button
      type="button"
      class="quiet"
      :disabled="busy || blocked"
      @click="load()"
    >
      刷新草稿与候选清单
    </button>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="!busy && !choices.length" class="muted">
      暂时没有未确认资料。可先在上方编辑并暂存草稿。
    </p>
    <label v-for="item in choices" :key="key(item.ref)">
      <input
        type="checkbox"
        :checked="chosen(item)"
        :disabled="busy || blocked || (!chosen(item) && selected.length >= 16)"
        @change="toggle(item)"
      />
      {{ labels[item.ref.kind] }}：{{ item.name }}
    </label>
    <button
      v-if="next"
      type="button"
      class="quiet"
      :disabled="busy || blocked"
      @click="load(true)"
    >
      加载更多未确认资料
    </button>
    <div v-if="selected.length">
      <p>已明确选中 {{ selected.length }} 项</p>
      <p v-for="item in selected" :key="key(item.ref)">
        {{ labels[item.ref.kind] }} · {{ item.name }}
        <button
          type="button"
          class="quiet"
          :disabled="busy || blocked"
          @click="toggle(item)"
        >
          移除所选 {{ item.name }}
        </button>
      </p>
    </div>
  </fieldset>
</template>

<style scoped>
.working-picker {
  border: 1px solid #dcd8cd;
  border-radius: 0.6rem;
  padding: 1rem;
}
label {
  display: block;
  margin: 0.6rem 0;
}
</style>
