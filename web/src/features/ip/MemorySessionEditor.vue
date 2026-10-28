<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type {
  MemoryItem,
  MemorySession,
  MemorySessionDocument,
} from "./memory";
import { copyMemory } from "./memory";
const props = defineProps<{
  session: MemorySession | null;
  draft?: MemorySessionDocument | null;
  items: MemoryItem[];
  blocked: boolean;
}>();
const emit = defineEmits<{
  save: [MemorySessionDocument];
  change: [MemorySessionDocument];
  dirty: [boolean];
}>();
const empty = (): MemorySessionDocument => ({
  title: "",
  goal: "",
  notes: "",
  focus_ids: [],
});
const document = ref<MemorySessionDocument>(
  copyMemory(props.draft ?? props.session?.document ?? empty()),
);
const baseline = JSON.stringify(props.session?.document ?? empty());
const dirty = computed(() => JSON.stringify(document.value) !== baseline);
watch(dirty, (value) => emit("dirty", value), { immediate: true });
watch(document, (value) => emit("change", copyMemory(value)), { deep: true });
</script>
<template>
  <form @submit.prevent="emit('save', copyMemory(document))">
    <fieldset :disabled="blocked">
      <legend>{{ session ? "本次工作的短期记录" : "新建私人工作会话" }}</legend>
      <label
        >工作会话名称<input v-model="document.title" required maxlength="120"
      /></label>
      <label
        >本次工作目标<textarea
          v-model="document.goal"
          rows="2"
          maxlength="4000"
        />
      </label>
      <label
        >工作笔记<textarea
          v-model="document.notes"
          rows="3"
          maxlength="16000"
        />
      </label>
      <label
        >本次重点记忆<select v-model="document.focus_ids" multiple>
          <option
            v-for="item in items.filter((i) => i.state !== 'revoked')"
            :key="item.id"
            :value="item.id"
          >
            {{ item.text.slice(0, 60) }}
          </option>
        </select></label
      >
      <p class="muted">
        笔记仅在此私人会话保留；保存不会采用记忆，也不会更新故事正史。
      </p>
      <button :disabled="!dirty">
        {{ session ? "保存工作记录" : "创建工作会话" }}
      </button>
    </fieldset>
  </form>
</template>
<style scoped>
fieldset {
  display: grid;
  gap: 0.8rem;
  border: 1px solid #d7ded5;
  border-radius: 8px;
}
</style>
