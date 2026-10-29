<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import {
  CHAPTER_MATCH_LIMIT,
  findChapterMatches,
  previewChapterReplacement,
  type ChapterReplacement,
  type ChapterTextChange,
} from "./chapterTextTools";

const props = defineProps<{
  text: string;
  context: string;
  disabled: boolean;
}>();
const emit = defineEmits<{
  change: [value: ChapterTextChange];
  locate: [value: { start: number; end: number; text: string }];
  close: [];
}>();
const query = ref("");
const replacement = ref("");
const index = ref(0);
const error = ref("");
const preview = ref<(ChapterReplacement & { context: string }) | null>(null);
const undo = ref<{ before: string; after: string; context: string } | null>(
  null,
);
const found = computed(() => {
  try {
    return { ...findChapterMatches(props.text, query.value), error: "" };
  } catch (cause) {
    return {
      total: 0,
      matches: [],
      truncated: false,
      error: cause instanceof Error ? cause.message : "文字暂时无法匹配。",
    };
  }
});
const canPreview = computed(
  () =>
    !props.disabled && !!found.value.total && query.value !== replacement.value,
);
const current = computed(() => found.value.matches[index.value]);
const canUndo = computed(
  () =>
    !!undo.value &&
    props.text === undo.value.after &&
    props.context === undo.value.context,
);
const examples = computed(
  () =>
    preview.value?.changes.slice(0, 5).map((match) => ({
      prefix: [...preview.value!.sourceText.slice(0, match.start)]
        .slice(-24)
        .join(""),
      before: preview.value!.sourceText.slice(match.start, match.end),
      suffix: [...preview.value!.sourceText.slice(match.end)]
        .slice(0, 24)
        .join(""),
    })) ?? [],
);
watch([query, replacement], () => {
  preview.value = null;
  error.value = "";
});
watch(query, () => {
  index.value = 0;
});
watch(index, () => {
  preview.value = null;
});
watch(
  [() => props.text, () => props.context],
  () => {
    preview.value = null;
    index.value = 0;
    if (!canUndo.value) undo.value = null;
  },
  { flush: "sync" },
);
function locate(offset = 0) {
  if (props.disabled || !found.value.matches.length) return;
  const count = found.value.matches.length;
  index.value = (index.value + offset + count) % count;
  const match = current.value;
  if (match) emit("locate", { ...match, text: props.text });
}
function prepare(mode: "one" | "all") {
  if (!canPreview.value) return;
  error.value = "";
  try {
    preview.value = {
      ...previewChapterReplacement(
        props.text,
        query.value,
        replacement.value,
        mode,
        index.value,
      ),
      context: props.context,
    };
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "无法预览替换。";
  }
}
async function apply() {
  const value = preview.value;
  if (
    props.disabled ||
    !value ||
    value.sourceText !== props.text ||
    value.context !== props.context ||
    value.query !== query.value ||
    value.replacement !== replacement.value ||
    (value.mode === "one" && value.changes[0]?.start !== current.value?.start)
  )
    return;
  undo.value = {
    before: value.sourceText,
    after: value.text,
    context: value.context,
  };
  preview.value = null;
  emit("change", {
    context: value.context,
    sourceText: value.sourceText,
    text: value.text,
    action: "replace",
  });
  await nextTick();
  if (props.text !== value.text) {
    undo.value = null;
    error.value =
      "当前章稿暂时不能替换，原正文保持原样，请核对保存状态后重新预览。";
  }
}
async function undoChange() {
  const value = undo.value;
  if (props.disabled || !value || !canUndo.value) return;
  undo.value = null;
  emit("change", {
    context: value.context,
    sourceText: value.after,
    text: value.before,
    action: "undo",
  });
  await nextTick();
  if (props.text !== value.before)
    error.value = "当前章稿已变化，未撤销正文，请核对后继续编辑。";
}
</script>

<template>
  <section class="panel text-tools" aria-label="章内查找与替换">
    <header>
      <h2>章内查找与替换</h2>
      <button class="quiet" @click="emit('close')">收起查找</button>
    </header>
    <p>
      精确匹配原文，区分大小写；替换只作用于当前章。修改正文后仍需核对账本、审稿和后续章节。
    </p>
    <div class="inputs">
      <label
        >查找原文<textarea
          v-model="query"
          rows="2"
          maxlength="2000"
          placeholder="可查找人物名、句子或换行"
        />
      </label>
      <label
        >替换为<textarea
          v-model="replacement"
          rows="2"
          maxlength="100000"
          placeholder="留空表示删除匹配文字"
        />
      </label>
    </div>
    <p role="status">
      共 {{ found.total }} 处{{ current ? ` · 当前第 ${index + 1} 处` : "" }}
    </p>
    <p v-if="found.truncated" role="status">
      仅定位前
      {{ CHAPTER_MATCH_LIMIT }} 处；请细化原文后再替换本章全部，避免部分替换。
    </p>
    <p v-if="error || found.error" role="alert" class="error">
      {{ error || found.error }}
    </p>
    <div class="tool-actions">
      <button :disabled="disabled || !current" @click="locate(-1)">
        上一处
      </button>
      <button :disabled="disabled || !current" @click="locate()">
        定位当前处
      </button>
      <button :disabled="disabled || !current" @click="locate(1)">
        下一处
      </button>
      <button :disabled="!canPreview" @click="prepare('one')">
        预览替换当前处
      </button>
      <button
        :disabled="!canPreview || found.truncated"
        @click="prepare('all')"
      >
        预览替换本章全部
      </button>
      <button v-if="canUndo" :disabled="disabled" @click="undoChange">
        撤销本次替换
      </button>
    </div>
    <section v-if="preview" class="replacement-preview" aria-label="替换预览">
      <h3>替换预览 · {{ preview.changes.length }} 处</h3>
      <p>
        {{
          preview.replacement
            ? "以下改动将加入当前私人稿。"
            : "将删除匹配文字，请核对前后衔接。"
        }}
        {{
          preview.changes.length > examples.length
            ? `仅展示前 ${examples.length} 处示例。`
            : ""
        }}
      </p>
      <ol>
        <li v-for="(example, i) in examples" :key="i">
          <p>
            原文：<span class="excerpt"
              >{{ example.prefix }}<mark>{{ example.before }}</mark
              >{{ example.suffix }}</span
            >
          </p>
          <p>
            替换后：<span class="excerpt"
              >{{ example.prefix
              }}<mark>{{ preview.replacement || "（删除）" }}</mark
              >{{ example.suffix }}</span
            >
          </p>
        </li>
      </ol>
      <button :disabled="disabled" @click="apply">确认应用替换</button>
      <button class="quiet" @click="preview = null">取消本次预览</button>
    </section>
  </section>
</template>

<style scoped>
.text-tools {
  display: grid;
  gap: 0.75rem;
}
header,
.tool-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}
header {
  justify-content: space-between;
}
h2,
p {
  margin: 0;
}
.inputs {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem;
}
label {
  display: grid;
  gap: 0.35rem;
  min-width: 0;
}
textarea {
  width: 100%;
  box-sizing: border-box;
  resize: vertical;
}
.replacement-preview {
  border-top: 1px solid #dce1e7;
  padding-top: 0.75rem;
}
.replacement-preview ol {
  padding-left: 1.4rem;
}
.replacement-preview li {
  margin-bottom: 0.75rem;
  overflow-wrap: anywhere;
}
.excerpt {
  white-space: pre-wrap;
}
@media (max-width: 600px) {
  .inputs {
    grid-template-columns: 1fr;
  }
}
</style>
