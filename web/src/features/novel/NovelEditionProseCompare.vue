<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import { APIError, message } from "../../api";
import type { ManuscriptChapter } from "./manuscriptTypes";
import {
  useEditionChapterReads,
  type EditionChapterText,
} from "./useEditionChapterReads";
import NovelProseDiff from "./NovelProseDiff.vue";
const props = defineProps<{
  bookId: string;
  chapterId: string;
  left: { id: string; name: string; chapter?: ManuscriptChapter };
  right: { id: string; name: string; chapter?: ManuscriptChapter };
}>();
const emit = defineEmits<{ close: []; denied: [status: number] }>();
const reads = useEditionChapterReads(props.bookId);
const result = ref<{
  left: EditionChapterText | null;
  right: EditionChapterText | null;
} | null>(null);
const busy = ref(false),
  error = ref("");
let sequence = 0;
async function load() {
  const current = ++sequence;
  busy.value = true;
  error.value = "";
  result.value = null;
  try {
    const settled = await Promise.allSettled([
      props.left.chapter?.included
        ? reads.read(props.left.id, props.chapterId, props.left.chapter)
        : Promise.resolve(null),
      props.right.chapter?.included
        ? reads.read(props.right.id, props.chapterId, props.right.chapter)
        : Promise.resolve(null),
    ]);
    if (!reads.active() || current !== sequence) return;
    const failures = settled.filter(
      (value): value is PromiseRejectedResult => value.status === "rejected",
    );
    const denied = failures.find(
      (value) =>
        value.reason instanceof APIError &&
        [401, 403].includes(value.reason.status),
    );
    if (denied) throw denied.reason;
    if (failures[0]) throw failures[0].reason;
    if (
      settled[0]?.status === "fulfilled" &&
      settled[1]?.status === "fulfilled"
    )
      result.value = { left: settled[0].value, right: settled[1].value };
  } catch (e) {
    if (!reads.active() || current !== sequence) return;
    error.value = message(e);
    if (e instanceof APIError && [401, 403].includes(e.status))
      emit("denied", e.status);
  } finally {
    if (reads.active() && current === sequence) busy.value = false;
  }
}
watch(
  () => [props.bookId, props.chapterId, props.left.id, props.right.id],
  () => {
    void load();
  },
);
onMounted(load);
</script>
<template>
  <section class="panel" aria-label="成书章节正文比较">
    <div class="actions">
      <h3>{{ left.chapter?.title ?? right.chapter?.title }} · 正文对照</h3>
      <button @click="emit('close')">关闭正文对照</button>
      <button :disabled="busy" @click="load">重新读取正文</button>
    </div>
    <p>
      按章节ID
      {{ chapterId }} 读取已保存稿本中的原文；不会修改稿本或当前私人稿。
    </p>
    <p v-if="busy" role="status">正在读取本章两版正文…</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <template v-if="result">
      <p v-if="!result.left" role="status">
        {{ left.name }}：此章不存在或未纳入正文，空白一侧不代表历史正文已删除。
      </p>
      <p v-if="!result.right" role="status">
        {{ right.name }}：此章不存在或未纳入正文，空白一侧不代表历史正文已删除。
      </p>
      <NovelProseDiff
        :left="result.left?.text ?? ''"
        :right="result.right?.text ?? ''"
        :left-label="left.name"
        :right-label="right.name"
      />
    </template>
  </section>
</template>
