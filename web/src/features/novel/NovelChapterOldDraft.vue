<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { APIError, message } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
import type { ChapterDraft } from "./chapterTypes";
import type { SavedEdition } from "./manuscriptTypes";
import {
  useEditionChapterReads,
  type EditionChapterText,
} from "./useEditionChapterReads";
import NovelProseDiff from "./NovelProseDiff.vue";

export interface OldDraftTake {
  text: string;
  resourceId: string;
  revision: number;
  hash: string;
  document: string;
}
const props = defineProps<{
  bookId: string;
  current: ChapterDraft;
  saved: boolean;
  disabled?: boolean;
  undoAvailable?: boolean;
}>();
const emit = defineEmits<{
  close: [];
  expired: [];
  take: [value: OldDraftTake];
  undo: [];
}>();
const lists = useBoundedReads({
  stopped: "已离开旧稿页面。",
  timeout: "成书稿本列表读取超时，可以重新读取。",
});
const reads = useEditionChapterReads(props.bookId);
const editions = ref<SavedEdition[]>([]),
  next = ref(""),
  editionId = ref("");
const loading = ref(false),
  busy = ref(false),
  error = ref("");
const old = ref<EditionChapterText | null>(null),
  acknowledged = ref(false);
const currentKey = computed(() =>
  JSON.stringify([
    props.current.resource.id,
    props.current.revision,
    props.current.hash,
    props.current.document,
  ]),
);
const canTake = computed(
  () =>
    !!old.value &&
    old.value.edition_id === editionId.value &&
    old.value.chapter.id === props.current.document.chapter_id &&
    props.saved &&
    !props.disabled &&
    !busy.value &&
    old.value.text !== props.current.document.text &&
    acknowledged.value,
);
let sequence = 0,
  listSequence = 0;
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && [401, 403].includes(e.status)) {
    ++sequence;
    ++listSequence;
    editions.value = [];
    next.value = "";
    editionId.value = "";
    old.value = null;
    acknowledged.value = false;
    busy.value = false;
    loading.value = false;
    if (e.status === 401) emit("expired");
  }
}
async function loadEditions(more = false) {
  if (loading.value) return;
  const current = ++listSequence;
  loading.value = true;
  error.value = "";
  try {
    const value = await lists.read<{ items: SavedEdition[]; next: string }>(
      `/novels/${encodeURIComponent(props.bookId)}/export/editions${more && next.value ? `?after=${encodeURIComponent(next.value)}` : ""}`,
    );
    if (!lists.active() || current !== listSequence) return;
    if (
      !value ||
      !Array.isArray(value.items) ||
      typeof value.next !== "string" ||
      value.items.some(
        (item) =>
          !item ||
          typeof item.id !== "string" ||
          typeof item.name !== "string" ||
          typeof item.published !== "boolean",
      )
    )
      throw new Error("成书稿本列表不完整，请重新读取。");
    const merged = more ? [...editions.value, ...value.items] : value.items;
    editions.value = [
      ...new Map(
        merged.filter((item) => item.published).map((item) => [item.id, item]),
      ).values(),
    ];
    next.value = value.next;
    if (!editions.value.some((item) => item.id === editionId.value))
      editionId.value = "";
  } catch (e) {
    if (lists.active() && current === listSequence) fail(e);
  } finally {
    if (lists.active() && current === listSequence) loading.value = false;
  }
}
async function loadText() {
  if (!editionId.value || busy.value) return;
  const current = ++sequence;
  const id = editionId.value;
  const chapter = props.current.document.chapter_id;
  old.value = null;
  acknowledged.value = false;
  error.value = "";
  busy.value = true;
  try {
    const value = await reads.read(id, chapter);
    if (reads.active() && current === sequence) old.value = value;
  } catch (e) {
    if (reads.active() && current === sequence) fail(e);
  } finally {
    if (reads.active() && current === sequence) busy.value = false;
  }
}
function take() {
  if (!canTake.value || !old.value) return;
  emit("take", {
    text: old.value.text,
    resourceId: props.current.resource.id,
    revision: props.current.revision,
    hash: props.current.hash,
    document: JSON.stringify(props.current.document),
  });
  acknowledged.value = false;
}
watch(
  editionId,
  () => {
    ++sequence;
    old.value = null;
    acknowledged.value = false;
    busy.value = false;
    error.value = "";
  },
  { flush: "sync" },
);
watch(
  currentKey,
  () => {
    acknowledged.value = false;
  },
  { flush: "sync" },
);
watch(
  () => [props.bookId, props.current.document.chapter_id],
  () => {
    ++sequence;
    ++listSequence;
    old.value = null;
    editions.value = [];
    editionId.value = "";
    next.value = "";
    busy.value = false;
    loading.value = false;
    void loadEditions();
  },
  { flush: "sync" },
);
onMounted(() => loadEditions());
</script>
<template>
  <section class="panel old-draft" aria-label="本章旧稿对照">
    <div class="actions">
      <h2>本章旧稿对照与取回</h2>
      <button @click="emit('close')">关闭旧稿对照</button>
    </div>
    <p>
      只读取已经保存的成书稿本。未存入稿本的私人修改没有可浏览的历史；取回只替换当前正文，保留当前章名、写作依据、视角、故事位置和参考资料。
    </p>
    <p>
      取回后沿当前编辑器自动暂存为私人稿；不重新记账、审稿或正式采用。替换前可先下载当前私人稿，本页保留一次撤销，离开页面后不再保留。
    </p>
    <div class="actions">
      <label
        >旧成书稿本<select v-model="editionId">
          <option value="">请选择已保存稿本</option>
          <option v-for="item in editions" :key="item.id" :value="item.id">
            {{ item.name }} · {{ item.id.slice(0, 8) }}
          </option>
        </select></label
      >
      <button :disabled="loading || busy" @click="loadEditions()">
        刷新旧稿本列表
      </button>
      <button
        v-if="next"
        :disabled="loading || busy"
        @click="loadEditions(true)"
      >
        加载更多旧稿本
      </button>
      <button :disabled="!editionId || busy" @click="loadText">
        {{ busy ? "正在读取旧正文…" : "读取本章旧正文" }}
      </button>
    </div>
    <p v-if="loading" role="status">正在读取已保存成书稿本…</p>
    <p v-else-if="!editions.length && !error" role="status">
      尚无可读取的已保存稿本，可在导出区把已正式采用稿保存为成书版本。
    </p>
    <p v-if="error" role="alert">{{ error }}</p>
    <template v-if="old">
      <p>
        旧稿：{{ old.chapter.volume }} / {{ old.chapter.title }} ·
        {{ old.chapter.characters.toLocaleString() }} 字符。按章节ID
        {{ old.chapter.id }} 定位，同名章节不会混用。
      </p>
      <NovelProseDiff
        :left="old.text"
        :right="current.document.text"
        left-label="成书旧正文"
        right-label="当前私人稿正文"
      />
      <p v-if="!saved" role="status">请先等当前私人稿保存完成，再确认取回。</p>
      <label class="confirm"
        ><input
          v-model="acknowledged"
          type="checkbox"
          :disabled="disabled || !saved || old.text === current.document.text"
        />我确认用旧正文替换当前私人稿正文，并沿现有流程自动暂存</label
      >
    </template>
    <div class="actions">
      <button :disabled="!canTake" @click="take">取回这版正文</button>
      <button
        v-if="undoAvailable"
        :disabled="disabled || !saved"
        @click="emit('undo')"
      >
        撤销这次旧稿取回
      </button>
    </div>
  </section>
</template>
<style scoped>
.old-draft {
  margin: 16px 0;
}
.old-draft select {
  max-width: 100%;
}
.confirm {
  display: flex;
  align-items: start;
  gap: 8px;
  margin: 14px 0;
}
.confirm input {
  width: auto;
}
</style>
