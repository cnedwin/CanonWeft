<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, computed, watch } from "vue";
import { api, APIError, message } from "../../api";
import type { PlanResource, PlanningOverview } from "./planningTypes";
const props = defineProps<{
  bookId: string;
  modelValue: string[];
  disabled: boolean;
  refreshKey: number;
}>();
const emit = defineEmits<{
  "update:modelValue": [value: string[]];
  materials: [];
  expired: [];
}>();
const sources = ref<PlanResource[]>([]),
  loading = ref(false),
  error = ref("");
let disposed = false;
const missing = computed(() =>
  props.modelValue.filter((id) => !sources.value.some((s) => s.id === id)),
);
async function refresh() {
  if (loading.value) return;
  loading.value = true;
  error.value = "";
  try {
    const data = await api<PlanningOverview>(`/novels/${props.bookId}/plans`);
    if (!disposed) sources.value = data.sources;
  } catch (e) {
    if (!disposed) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (!disposed) loading.value = false;
  }
}
function toggle(id: string, event: Event) {
  const checked = (event.target as HTMLInputElement).checked;
  emit(
    "update:modelValue",
    checked
      ? [...props.modelValue, id]
      : props.modelValue.filter((v) => v !== id),
  );
}
onMounted(refresh);
watch(() => props.refreshKey, refresh);
onBeforeUnmount(() => {
  disposed = true;
});
</script>
<template>
  <fieldset :disabled="disabled" class="source-picker">
    <legend>角色创作参考资料</legend>
    <p>
      所选资料会全文交给助手，每次最多12份；已确认的世界与故事规划自动加入。任务准备时固定资料版本，全文超出容量时明确提示。
    </p>
    <div class="actions">
      <button type="button" @click="emit('materials')">
        上传、编辑与采用本书资料</button
      ><button type="button" :disabled="loading" @click="refresh">
        刷新参考资料
      </button>
    </div>
    <p v-if="loading" role="status">正在读取参考资料…</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="!loading && !error && !sources.length">
      暂无已采用资料。可上传文件或填写文字，核对解析结果并确认采用后再选择。
    </p>
    <label v-for="source in sources" :key="source.id"
      ><input
        type="checkbox"
        :value="source.id"
        :checked="modelValue.includes(source.id)"
        :disabled="!modelValue.includes(source.id) && modelValue.length >= 12"
        @change="toggle(source.id, $event)"
      />{{ source.name }} · 第{{ source.revision }}版</label
    >
    <p v-if="!loading && !error && missing.length">
      有{{ missing.length }}份所选资料当前不可用。<button
        type="button"
        @click="
          emit(
            'update:modelValue',
            modelValue.filter((id) => !missing.includes(id)),
          )
        "
      >
        移除不可用参考资料
      </button>
    </p>
    <p>
      已选择 {{ modelValue.length }} / 12
      份。资料用于创作参考，生成的人设仍须由你确认。
    </p>
  </fieldset>
</template>
<style scoped>
.source-picker {
  display: grid;
  gap: 0.5rem;
  margin: 1rem 0;
  padding: 1rem;
}
label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
input {
  width: auto;
  flex: 0 0 auto;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
</style>
