<script setup lang="ts">
import { computed, watch } from "vue";
import type { BookReviewEditTarget } from "./bookReviewTypes";
import { reviewQuotePositions, type QuotePosition } from "./reviewAnchor";
const props = defineProps<{
  target: BookReviewEditTarget;
  text: string;
  hash: string;
  saved: boolean;
  disabled: boolean;
}>();
const emit = defineEmits<{
  locate: [value: { start: number; end: number; text: string }];
  refine: [instruction: string];
  close: [];
}>();
const matches = computed(() =>
  reviewQuotePositions(props.text, props.target.quote),
);
function locate(position: QuotePosition) {
  if (!props.disabled)
    emit("locate", {
      start: position.start,
      end: position.end,
      text: props.text,
    });
}
function refine() {
  const text = `处理整卷/全书审稿问题：${props.target.title}\n审稿依据与解释：${props.target.explanation}\n修改建议：${props.target.suggestion}\n原报告引用：${props.target.quote}\n请核对当前稿再修改，不把旧报告视为当前事实；保留选定范围之外的正文。`;
  emit("refine", Array.from(text).slice(0, 3900).join(""));
}
let attempted = false;
watch(
  () => props.disabled,
  (disabled) => {
    if (disabled || attempted) return;
    attempted = true;
    if (matches.value.total === 1) locate(matches.value.positions[0]);
  },
  { immediate: true, flush: "post" },
);
</script>
<template>
  <section class="panel review-anchor" aria-label="本次审稿修改任务">
    <header>
      <h2>{{ target.title }}</h2>
      <button :disabled="disabled" @click="emit('close')">收起问题提示</button>
    </header>
    <p>{{ target.explanation }}</p>
    <p>建议：{{ target.suggestion }}</p>
    <blockquote aria-label="审稿原引用">{{ target.quote }}</blockquote>
    <p v-if="!saved" role="status">
      当前有未保存的修改。定位使用当前编辑文字；处置与复核须保存后返回报告核对。
    </p>
    <p v-else-if="!target.reviewed_hash" role="status">
      原报告缺少可比的章稿版本，不能确认当前稿与审稿稿本相同。
    </p>
    <p v-else-if="hash !== target.reviewed_hash" role="status">
      当前已保存稿与原审稿版本不同；引文仍出现也不代表原问题已解决。
    </p>
    <p v-else>当前已保存稿与原审稿章节版本一致；修改后仍需返回报告核对。</p>
    <p v-if="!matches.total" role="status">
      当前稿未找到这段原引用，请结合建议手动核对。引用消失不能作为问题已经解决的依据。
    </p>
    <template v-else-if="matches.total === 1"
      ><p>当前文字有一处精确匹配。</p>
      <button :disabled="disabled" @click="locate(matches.positions[0])">
        定位原引用
      </button></template
    >
    <template v-else
      ><p>当前文字有 {{ matches.total }} 处相同引用，请明确选择位置。</p>
      <ol>
        <li
          v-for="(position, index) in matches.positions"
          :key="position.start"
        >
          <p>{{ position.context }}</p>
          <button :disabled="disabled" @click="locate(position)">
            定位第 {{ index + 1 }} 处
          </button>
        </li>
      </ol>
      <p v-if="matches.total > matches.positions.length">
        仅展示前 {{ matches.positions.length }} 处，其余位置请在正文中手动选择。
      </p></template
    >
    <button :disabled="disabled" @click="refine">带入原地精修要求</button>
    <p>
      此操作只补充精修要求，不开始生成。保存后返回同一审稿报告记录处理和人工复核；原报告始终保留。
    </p>
  </section>
</template>
<style scoped>
.review-anchor {
  display: grid;
  gap: 0.7rem;
}
.review-anchor header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  flex-wrap: wrap;
}
h2,
p {
  margin: 0.2rem 0;
}
blockquote {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  margin: 0;
  padding: 0.8rem;
  border-left: 3px solid #c7af83;
}
li {
  margin: 0.7rem 0;
}
li p {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
