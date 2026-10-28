<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { api, APIError, message } from "../../api";
import type { DependencyRef } from "../ip/dependencies";
const props = defineProps<{
  root: string;
  revision: number;
  hash: string;
  ready: boolean;
  saved: boolean;
}>();
const emit = defineEmits<{ expired: [] }>();
interface WritingCard {
  title: string;
  goal: string;
  mainline: string;
  theme: string;
  reader_promise: string;
  perspective: string;
  people: string;
  scene_goals: string[];
  guardrails: string[];
  recent_plan: {
    id: string;
    title: string;
    goal: string;
    change: string;
    transition: string;
  }[];
  sources: DependencyRef[];
  notes: string[];
}
const card = ref<WritingCard | null>(null),
  error = ref(""),
  loading = ref(false),
  expanded = ref(false);
let sequence = 0,
  disposed = false;
watch([() => props.hash, () => props.revision, () => props.saved], () => {
  ++sequence;
  card.value = null;
  loading.value = false;
  error.value = "";
});
async function load() {
  if (!props.ready || !props.saved || loading.value) return;
  const request = ++sequence,
    revision = props.revision,
    hash = props.hash;
  loading.value = true;
  error.value = "";
  try {
    const value = await api<{
      revision: number;
      hash: string;
      card: WritingCard;
    }>(`${props.root}/writing-card`);
    if (disposed || request !== sequence) return;
    if (value.revision !== revision || value.hash !== hash)
      throw new Error("写作依据已经变化，请重新读取写作卡。");
    card.value = value.card;
  } catch (e) {
    if (!disposed && request === sequence) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (!disposed && request === sequence) loading.value = false;
  }
}
function toggle(event: Event) {
  expanded.value = (event.target as HTMLDetailsElement).open;
  if (expanded.value && !card.value) void load();
}
onBeforeUnmount(() => {
  disposed = true;
  ++sequence;
});
</script>
<template>
  <details class="panel" @toggle="toggle">
    <summary>本章写作卡与承接</summary>
    <p v-if="!ready">请先更新并确认写作依据；原稿仍会保留。</p>
    <p v-else-if="!saved">请先暂存当前修改，再读取与此版对应的写作卡。</p>
    <p v-else-if="loading" role="status">正在整理本章已确认材料…</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <button
      v-if="expanded && ready && saved && !loading"
      class="quiet"
      @click="load"
    >
      {{ card ? "刷新写作卡" : "读取写作卡" }}
    </button>
    <div v-if="card" class="writing-card">
      <h3>{{ card.title }}</h3>
      <dl>
        <dt>本章目标</dt>
        <dd>{{ card.goal }}</dd>
        <dt>全书主线</dt>
        <dd>{{ card.mainline }}</dd>
        <dt>主题与读者期待</dt>
        <dd>
          {{ [card.theme, card.reader_promise].filter(Boolean).join(" · ") }}
        </dd>
        <dt>视角与人物</dt>
        <dd>
          {{ [card.perspective, card.people].filter(Boolean).join(" · ") }}
        </dd>
      </dl>
      <h4>场景目标</h4>
      <ul>
        <li v-for="(goal, i) in card.scene_goals" :key="i">{{ goal }}</li>
      </ul>
      <h4>不得改变的约定</h4>
      <ul>
        <li v-for="(rule, i) in card.guardrails" :key="i">{{ rule }}</li>
      </ul>
      <h4>近期规划承接</h4>
      <p class="muted">以下是规划安排；已发生剧情以采用的正文与账本为准。</p>
      <ul>
        <li v-for="item in card.recent_plan" :key="item.id">
          <strong>{{ item.title }}</strong> ·
          {{
            [item.goal, item.change, item.transition].filter(Boolean).join("；")
          }}
        </li>
      </ul>
      <ul>
        <li v-for="(note, i) in card.notes" :key="i">{{ note }}</li>
      </ul>
      <p class="muted">
        关联
        {{
          card.sources.length
        }}
        项确认材料；生成时仍由系统核对版本、视角与上下文预算。
      </p>
    </div>
  </details>
</template>
<style scoped>
summary {
  cursor: pointer;
  font-weight: 600;
}
dl {
  display: grid;
  grid-template-columns: 8rem 1fr;
  gap: 0.5rem;
}
dd {
  margin: 0;
  white-space: pre-wrap;
}
@media (max-width: 600px) {
  dl {
    grid-template-columns: 1fr;
  }
}
</style>
