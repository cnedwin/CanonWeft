<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { APIError, message } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
import type { ChapterReadiness } from "./manuscriptCatalog";

const props = defineProps<{
  bookId: string;
  chapterId: string;
  blocked: boolean;
}>();
const emit = defineEmits<{ navigate: [id: string]; expired: [] }>();
const { read, active } = useBoundedReads({
  stopped: "章节顺序读取已停止。",
  timeout: "章节顺序读取超时，当前章稿仍保留，可以重试。",
});
const rows = ref<ChapterReadiness[]>([]);
const loading = ref(false),
  error = ref("");
const query = ref(""),
  selected = ref("");
let sequence = 0;
const current = computed(() =>
  rows.value.findIndex((row) => row.id === props.chapterId),
);
const previous = computed(() =>
  current.value > 0 ? rows.value[current.value - 1] : undefined,
);
const next = computed(() =>
  current.value >= 0 ? rows.value[current.value + 1] : undefined,
);
const matches = computed(() => {
  const text = query.value.trim().toLocaleLowerCase();
  return rows.value.filter(
    (row) =>
      !text || `${row.title}\n${row.id}`.toLocaleLowerCase().includes(text),
  );
});
const options = computed(() => matches.value.slice(0, 200));
const unavailable = computed(
  () => props.blocked || loading.value || !!error.value || current.value < 0,
);
function open(id: string | undefined) {
  if (unavailable.value || !id || id === props.chapterId) return;
  const row = rows.value.find((value) => value.id === id);
  if (row?.ready) emit("navigate", row.id);
}
function decode(value: unknown): ChapterReadiness[] {
  if (!value || typeof value !== "object" || !("items" in value))
    throw new Error("章节目录格式或身份不完整，请重新读取。");
  const items = value.items;
  if (items === null) return [];
  if (!Array.isArray(items) || items.length > 10000)
    throw new Error("章节目录格式或身份不完整，请重新读取。");
  const seen = new Set<string>();
  return items.map((item: unknown) => {
    if (
      !item ||
      typeof item !== "object" ||
      !("id" in item) ||
      typeof item.id !== "string" ||
      !item.id ||
      seen.has(item.id) ||
      !("title" in item) ||
      typeof item.title !== "string" ||
      !("ready" in item) ||
      typeof item.ready !== "boolean" ||
      !("reason" in item) ||
      typeof item.reason !== "string"
    )
      throw new Error("章节目录格式或身份不完整，请重新读取。");
    seen.add(item.id);
    return {
      id: item.id,
      title: item.title,
      ready: item.ready,
      reason: item.reason,
    };
  });
}
async function refresh() {
  const request = ++sequence;
  loading.value = true;
  error.value = "";
  selected.value = "";
  try {
    const value = await read<unknown>(
      `/novels/${encodeURIComponent(props.bookId)}/writing-batches/chapters`,
    );
    if (!active() || request !== sequence) return;
    rows.value = decode(value);
  } catch (cause) {
    if (!active() || request !== sequence) return;
    rows.value = [];
    error.value = message(cause);
    if (cause instanceof APIError && cause.status === 401) emit("expired");
  } finally {
    if (active() && request === sequence) loading.value = false;
  }
}
watch(query, () => {
  selected.value = "";
});
watch(
  () => props.bookId,
  () => {
    rows.value = [];
    query.value = "";
    void refresh();
  },
  { immediate: true },
);
</script>

<template>
  <section class="panel chapter-navigation" aria-label="章节跳转">
    <div class="navigation-actions">
      <strong>章节跳转</strong>
      <span v-if="current >= 0"
        >第 {{ current + 1 }} / {{ rows.length }} 章</span
      >
      <button
        type="button"
        :disabled="unavailable || !previous?.ready"
        :title="
          previous
            ? `${previous.title}${previous.reason ? ' · ' + previous.reason : ''}`
            : '已到首章'
        "
        @click="open(previous?.id)"
      >
        上一章
      </button>
      <button
        type="button"
        :disabled="unavailable || !next?.ready"
        :title="
          next
            ? `${next.title}${next.reason ? ' · ' + next.reason : ''}`
            : '已到末章'
        "
        @click="open(next?.id)"
      >
        下一章
      </button>
      <button type="button" :disabled="loading" @click="refresh">
        刷新章节顺序
      </button>
    </div>
    <p v-if="loading" role="status">正在读取规划中的章节顺序…</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-else-if="!loading && current < 0" role="alert">
      当前章已不在最新规划目录，原稿仍保留，请返回规划核对后再跳转。
    </p>
    <div class="navigation-actions">
      <label
        >查找要打开的章节<input
          v-model="query"
          type="search"
          maxlength="200"
          placeholder="章节标题或编号"
      /></label>
      <label
        >目标章节<select
          v-model="selected"
          aria-label="目标章节"
          :disabled="loading || !!error"
        >
          <option value="">请选择章节</option>
          <option
            v-for="row in options"
            :key="row.id"
            :value="row.id"
            :disabled="!row.ready || row.id === chapterId"
          >
            {{ row.title }} · {{ row.id
            }}{{ row.ready ? "" : " · " + row.reason }}
          </option>
        </select></label
      >
      <button
        type="button"
        :disabled="
          unavailable ||
          !selected ||
          !rows.find((row) => row.id === selected)?.ready ||
          selected === chapterId
        "
        @click="open(selected)"
      >
        打开所选章
      </button>
    </div>
    <p class="muted">
      找到 {{ matches.length }} 章{{
        matches.length > 200 ? "，显示前 200 章，请细化关键词" : ""
      }}。顺序来自当前规划；未就绪章节请先确认细纲。
    </p>
    <p v-if="blocked" role="status">
      当前章有未保存或待核对的稿件、进行中的操作，或未处理的精修备注。请先处理后再跳转，原输入仍保留。
    </p>
  </section>
</template>

<style scoped>
.navigation-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: 0.65rem;
}
.navigation-actions label {
  display: grid;
  gap: 0.3rem;
  flex: 1 1 12rem;
  min-width: 0;
}
.navigation-actions select,
.navigation-actions input {
  width: 100%;
  min-width: 0;
}
</style>
