<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { api, APIError, message } from "../../api";
import type { Entity } from "./types";
import {
  copyMemory,
  locateMemorySource,
  memoryKinds,
  summaryLevels,
  type MemoryItem,
  type MemoryResource,
  type MemoryResourceDetail,
  type MemorySource,
} from "./memory";
const props = defineProps<{
  item: MemoryItem | null;
  project: string;
  branch: string;
  session: string;
  resources: MemoryResource[];
  items: MemoryItem[];
  entities: Entity[];
  blocked: boolean;
}>();
const emit = defineEmits<{
  save: [MemoryItem];
  dirty: [boolean];
  busy: [boolean];
  expired: [];
  discard: [];
}>();
const draft = ref<MemoryItem>(
  copyMemory(
    props.item ?? {
      id: "",
      revision: "",
      project: props.project,
      branch: props.branch,
      kind: "theme",
      state: "candidate",
      visibility: "author",
      known_by: [],
      from: 0,
      until: null,
      reveal_at: 0,
      control: true,
      pinned: false,
      text: "",
      sources: [],
      dependencies: [],
      score: 0,
      timeline: "main",
    },
  ),
);
draft.value.sources ??= [];
const baseline = JSON.stringify(draft.value);
const selectedResource = ref("");
const detail = ref<MemoryResourceDetail | null>(null);
const snippet = ref("");
const error = ref("");
const loading = ref(false);
const dirty = computed(
  () => JSON.stringify(draft.value) !== baseline || !!snippet.value,
);
const unavailable = computed(() => props.blocked || loading.value);
let sequence = 0;
let disposed = false;
watch(dirty, (value) => emit("dirty", value), { immediate: true });
watch(loading, (value) => emit("busy", value));
watch(selectedResource, async (id) => {
  const ticket = ++sequence;
  detail.value = null;
  snippet.value = "";
  error.value = "";
  if (!id) return;
  loading.value = true;
  try {
    const resource = await api<MemoryResourceDetail>(
      `/projects/${props.project}/resources/${id}`,
    );
    if (disposed || ticket !== sequence) return;
    if (
      resource.id !== id ||
      !resource.head ||
      resource.head !== props.resources.find((r) => r.id === id)?.head
    )
      throw new Error("来源版本已变化，请刷新记忆列表后重新选择资料。");
    detail.value = resource;
  } catch (e) {
    if (disposed || ticket !== sequence) return;
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    if (!disposed && ticket === sequence) loading.value = false;
  }
});
onBeforeUnmount(() => {
  disposed = true;
  sequence++;
});
function selectText(event: Event) {
  const input = event.target as HTMLTextAreaElement;
  if (input.selectionEnd > input.selectionStart)
    snippet.value = input.value.slice(input.selectionStart, input.selectionEnd);
}
function sourceName(source: MemorySource) {
  return (
    props.resources.find((r) => r.id === source.resource)?.name ??
    "来源资料不可用"
  );
}
function outdated(source: MemorySource) {
  return (
    props.resources.find((r) => r.id === source.resource)?.head !==
    source.revision
  );
}
function addSource() {
  error.value = "";
  if (unavailable.value || !detail.value) return;
  try {
    const source = locateMemorySource(detail.value, snippet.value);
    if (
      draft.value.sources.some(
        (s) => JSON.stringify(s) === JSON.stringify(source),
      )
    )
      throw new Error("此原文片段已经加入来源。");
    draft.value.sources.push(source);
    snippet.value = "";
  } catch (e) {
    error.value = message(e);
  }
}
function save() {
  error.value = "";
  if (unavailable.value) return;
  if (!props.session) {
    error.value = "请先选择或创建一个私人工作会话。";
    return;
  }
  if (
    !draft.value.text.trim() ||
    (!draft.value.sources.length && !draft.value.summary?.inputs?.length)
  ) {
    error.value = "请填写记忆内容，并至少加入一个已确认原文片段。";
    return;
  }
  if (snippet.value) {
    error.value = "原文片段尚未加入来源，请先加入或清空该片段。";
    return;
  }
  if (draft.value.sources.some(outdated)) {
    error.value = "来源已过期或不可用，请移除旧来源并重新引用当前资料。";
    return;
  }
  if (
    draft.value.visibility === "restricted" &&
    !draft.value.known_by?.length
  ) {
    error.value = "请为限定知情角色的记忆选择至少一个角色。";
    return;
  }
  if (draft.value.until != null && draft.value.until <= draft.value.from) {
    error.value = "有效终点必须晚于起点。";
    return;
  }
  emit("save", {
    ...copyMemory(draft.value),
    session: props.session,
    timeline: draft.value.timeline || "main",
  });
}
</script>
<template>
  <form
    class="memory-editor"
    data-testid="memory-editor"
    @submit.prevent="save"
  >
    <fieldset :disabled="unavailable">
      <legend>{{ item ? "修订这条记忆" : "新增记忆候选" }}</legend>
      <p
        v-if="item && ['published', 'adopted_plan'].includes(item.state)"
        class="notice"
      >
        修改后会保存为未确认候选，需要重新明确采用；不会直接改写正史。
      </p>
      <label
        >记忆类别<select v-model="draft.kind" :disabled="!!draft.summary">
          <option v-for="(name, kind) in memoryKinds" :key="kind" :value="kind">
            {{ name }}
          </option>
        </select></label
      >
      <p v-if="draft.summary">
        {{ summaryLevels[draft.summary.level] }}摘要：{{
          draft.summary.title
        }}。修改保留原有来源版本；下层资料过期时，请重新整理。
      </p>
      <label
        >记忆内容<textarea
          v-model="draft.text"
          required
          rows="4"
          maxlength="64000"
        />
      </label>
      <div class="fields">
        <label
          >故事生效起点<input
            v-model.number="draft.from"
            type="number"
            required
            step="1"
        /></label>
        <label
          >故事有效终点（不含，可留空）<input
            :value="draft.until ?? ''"
            type="number"
            step="1"
            @input="
              draft.until =
                ($event.target as HTMLInputElement).value === ''
                  ? null
                  : Number(($event.target as HTMLInputElement).value)
            "
        /></label>
        <label
          >向读者揭示位置<input
            v-model.number="draft.reveal_at"
            type="number"
            required
            step="1"
        /></label>
      </div>
      <p class="muted">
        位置使用作品统一的顺序编号；故事时间与叙述顺序分别记录，支持倒叙。
      </p>
      <label
        >记忆可见范围<select v-model="draft.visibility">
          <option value="author">作者可见</option>
          <option value="public">可公开表达</option>
          <option value="restricted">仅指定角色知情</option>
        </select></label
      >
      <label v-if="draft.visibility === 'restricted'"
        >知情角色<select v-model="draft.known_by" multiple>
          <option
            v-for="entity in entities"
            :key="entity.id"
            :value="entity.id"
          >
            {{ entity.name }}
          </option>
        </select></label
      >
      <label class="check"
        ><input
          v-model="draft.control"
          type="checkbox"
        />作为隐藏创作约束，不直接当作已发生事实</label
      >
      <label class="check"
        ><input
          v-model="draft.pinned"
          type="checkbox"
        />符合使用范围时必须纳入上下文</label
      >
      <label
        >前置记忆<select v-model="draft.dependencies" multiple>
          <option
            v-for="entry in items.filter(
              (i) => i.id !== item?.id && i.state !== 'revoked',
            )"
            :key="entry.id"
            :value="entry.id"
          >
            {{ memoryKinds[entry.kind] }} · {{ entry.text.slice(0, 55) }}
          </option>
        </select></label
      >
      <h4>引用已确认原文</h4>
      <label
        >来源资料<select v-model="selectedResource">
          <option value="">请选择已确认资料</option>
          <option
            v-for="resource in resources"
            :key="resource.id"
            :value="resource.id"
          >
            {{ resource.name }}
          </option>
        </select></label
      >
      <p v-if="!resources.length" class="muted">
        暂无已确认资料。先在上方资料区采用并发布原文，再刷新此面板。
      </p>
      <p v-if="loading" role="status">正在读取来源…</p>
      <template v-if="detail">
        <label
          >来源原文（可选中片段）<textarea
            :value="detail.text"
            readonly
            rows="5"
            @select="selectText"
          />
        </label>
        <label>引用原文片段<textarea v-model="snippet" rows="2" /></label>
        <button type="button" class="quiet" @click="addSource">
          加入来源片段
        </button>
      </template>
      <ul class="source-list">
        <li v-for="(source, index) in draft.sources" :key="index">
          {{ sourceName(source) }} · 第 {{ source.start_rune + 1 }}—{{
            source.end_rune
          }}
          字
          <strong v-if="outdated(source)">（来源过期或不可用）</strong>
          <button
            type="button"
            class="quiet"
            :aria-label="`移除来源 ${index + 1}`"
            @click="draft.sources.splice(index, 1)"
          >
            移除
          </button>
        </li>
      </ul>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <p v-if="!session" class="notice">
        请先选择或创建工作会话，候选会保存在该私人会话内。
      </p>
      <div class="actions">
        <button :disabled="!session">保存记忆候选</button
        ><button type="button" class="quiet" @click="emit('discard')">
          放弃本次编辑
        </button>
      </div>
    </fieldset>
  </form>
</template>
<style scoped>
fieldset {
  display: grid;
  gap: 0.9rem;
  border: 1px solid #d7ded5;
  border-radius: 8px;
  min-width: 0;
}
.fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 0.8rem;
}
.check {
  display: flex;
  align-items: center;
}
.check input {
  width: auto;
}
.source-list {
  padding-left: 1.3rem;
}
.source-list li {
  margin: 0.6rem 0;
}
</style>
