<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { APIError, message } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
import NovelProseDiff from "./NovelProseDiff.vue";
import type { ChapterDraft } from "./chapterTypes";
import {
  classifyChapterSave,
  type ChapterSaveAttempt,
  type ChapterSaveResolution,
} from "./chapterSaveRecovery";

const props = defineProps<{
  root: string;
  attempt: ChapterSaveAttempt;
  current: ChapterDraft;
  disabled: boolean;
}>();
const emit = defineEmits<{
  resolve: [value: ChapterSaveResolution];
  expired: [];
}>();
const reads = useBoundedReads({
  stopped: "保存核对读取已停止。",
  timeout: "保存核对读取超时，当前稿仍在本页，尚未重发保存。",
});
const checking = ref(false),
  error = ref(""),
  remote = ref<ChapterDraft | null>(null),
  accepted = ref(false);
const currentDocument = computed(() => JSON.stringify(props.current.document));
const status = computed(() =>
  remote.value ? classifyChapterSave(props.attempt, remote.value) : null,
);
watch(
  [() => props.attempt.id, currentDocument],
  () => {
    remote.value = null;
    accepted.value = false;
  },
  { flush: "sync" },
);
async function check() {
  if (props.disabled || checking.value) return;
  const attempt = props.attempt;
  checking.value = true;
  remote.value = null;
  accepted.value = false;
  error.value = "";
  try {
    const value = await reads.read<ChapterDraft>(
      `${props.root}/${attempt.resourceId}/draft`,
    );
    if (!reads.active() || props.attempt.id !== attempt.id) return;
    if (classifyChapterSave(attempt, value) === "committed")
      emit("resolve", {
        kind: "committed",
        attemptId: attempt.id,
        remote: value,
        currentDocument: currentDocument.value,
      });
    else remote.value = value;
  } catch (cause) {
    if (!reads.active() || props.attempt.id !== attempt.id) return;
    error.value = message(cause);
    if (cause instanceof APIError && cause.status === 401) emit("expired");
  } finally {
    if (reads.active()) checking.value = false;
  }
}
function retry() {
  if (
    props.disabled ||
    checking.value ||
    !accepted.value ||
    !remote.value ||
    status.value !== "unchanged"
  )
    return;
  emit("resolve", {
    kind: "retry",
    attemptId: props.attempt.id,
    remote: remote.value,
    currentDocument: currentDocument.value,
  });
}
</script>

<template>
  <section class="panel save-recovery" aria-label="章稿保存核对">
    <h2>上次保存需要核对</h2>
    <p>
      没有收到可确认的保存回执。本页稿仍保留，可以继续手写或下载；章内替换与后续保存暂缓，避免使用旧版本重发。
    </p>
    <button :disabled="disabled || checking" @click="check">
      {{ checking ? "正在核对已存稿…" : "核对上次保存" }}
    </button>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <template v-if="remote && status === 'unchanged'">
      <p>
        已存稿仍是上次保存前的版本，尚不能据此断定原请求失败。请核对差异；如需继续，可明确按已读版本保存本页稿，版本冲突时仍会保留本页内容。
      </p>
      <dl>
        <dt>已存稿</dt>
        <dd>
          {{ remote.document.title }} · 故事位置
          {{ remote.document.story_time }}
        </dd>
        <dt>本页稿</dt>
        <dd>
          {{ current.document.title }} · 故事位置
          {{ current.document.story_time }}
        </dd>
      </dl>
      <NovelProseDiff
        :left="remote.document.text"
        :right="current.document.text"
        left-label="已存正文"
        right-label="本页正文"
      />
      <label
        ><input
          v-model="accepted"
          type="checkbox"
          :disabled="disabled || checking"
        />我已核对，以刚读取的版本为基础保存本页稿</label
      >
      <button :disabled="disabled || checking || !accepted" @click="retry">
        按已读版本保存本页稿
      </button>
    </template>
    <p v-else-if="remote && status === 'diverged'" role="alert">
      已存稿与上次提交依据不一致，不能自动覆盖或宣称保存成功。请先下载本页稿，再重新打开章节核对其他修改；当前输入继续保留。
    </p>
  </section>
</template>

<style scoped>
.save-recovery {
  display: grid;
  gap: 0.75rem;
  border-color: #a66d12;
}
h2,
p {
  margin: 0;
}
dl {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 0.5rem;
}
dd {
  margin: 0;
  overflow-wrap: anywhere;
}
label {
  display: flex;
  align-items: start;
  gap: 0.5rem;
}
input {
  width: auto;
  flex: none;
}
</style>
