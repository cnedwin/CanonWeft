<script setup lang="ts">
import { computed, watch } from "vue";
import {
  chapterEvidencePosition,
  type ChapterEvidenceTarget,
} from "./chapterEvidence";
import { reviewQuotePositions, type QuotePosition } from "./reviewAnchor";
const props = defineProps<{
  target: ChapterEvidenceTarget;
  label?: string;
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
const ledgerEvidence = computed(() => props.label === "账本证据");
const proseEvidence = computed(() => props.label === "正文查找");
const pinnedPosition = computed(() =>
  chapterEvidencePosition(props.text, props.target, props.hash, props.saved),
);
const hasPinnedPosition = computed(
  () =>
    props.target.match_start !== undefined ||
    props.target.match_end !== undefined,
);
function locate(position: Pick<QuotePosition, "start" | "end">) {
  if (!props.disabled)
    emit("locate", {
      start: position.start,
      end: position.end,
      text: props.text,
    });
}
function refine() {
  const text = ledgerEvidence.value
    ? `核对账本证据：${props.target.title}\n记录依据与解释：${props.target.explanation}\n修改建议：${props.target.suggestion}\n账本原引用：${props.target.quote}\n请先核对当前稿与账本来源，不把旧记录的解释视为当前事实；保留选定范围之外的正文。账本须另行人工核对，本次要求不会自动修改账本。`
    : proseEvidence.value
      ? `核对正文查找命中：${props.target.title}\n查找上下文：${props.target.explanation}\n修改要求：${props.target.suggestion}\n本次查找原文：${props.target.quote}\n请核对当前正文与所选位置后修改；保留选定范围之外的正文。字面命中不代表语义影响范围，不自动全书替换或改写记忆。`
      : `处理整卷/全书审稿问题：${props.target.title}\n审稿依据与解释：${props.target.explanation}\n修改建议：${props.target.suggestion}\n原报告引用：${props.target.quote}\n请核对当前稿再修改，不把旧报告视为当前事实；保留选定范围之外的正文。`;
  emit("refine", Array.from(text).slice(0, 3900).join(""));
}
let attempted = false;
watch(
  () => props.disabled,
  (disabled) => {
    if (disabled || attempted) return;
    attempted = true;
    if (pinnedPosition.value) locate(pinnedPosition.value);
    else if (matches.value.total === 1) locate(matches.value.positions[0]);
  },
  { immediate: true, flush: "post" },
);
</script>
<template>
  <section
    class="panel review-anchor"
    :aria-label="label || '本次审稿修改任务'"
  >
    <header>
      <h2>{{ target.title }}</h2>
      <button :disabled="disabled" @click="emit('close')">
        {{
          ledgerEvidence
            ? "收起证据提示"
            : proseEvidence
              ? "收起查找提示"
              : "收起问题提示"
        }}
      </button>
    </header>
    <p>{{ target.explanation }}</p>
    <p>建议：{{ target.suggestion }}</p>
    <blockquote
      :aria-label="
        ledgerEvidence
          ? '账本原引用'
          : proseEvidence
            ? '查找原文'
            : '审稿原引用'
      "
    >
      {{ target.quote }}
    </blockquote>
    <p v-if="!saved" role="status">
      {{
        ledgerEvidence
          ? "当前有未保存的修改。定位使用当前编辑文字；请保存后返回剧情账本核对来源与记录。"
          : proseEvidence
            ? "当前有未保存的修改。原命中位置已停用，定位使用当前编辑文字；请保存后返回正文查找重新读取。"
            : "当前有未保存的修改。定位使用当前编辑文字；处置与复核须保存后返回报告核对。"
      }}
    </p>
    <p v-else-if="!target.reviewed_hash" role="status">
      {{
        ledgerEvidence
          ? "账本记录缺少可比的章稿版本，不能确认当前稿与账本来源相同。"
          : proseEvidence
            ? "查找结果缺少可比的章稿版本，不能确认当前稿与查找来源相同；请重新查找或手动核对。"
            : "原报告缺少可比的章稿版本，不能确认当前稿与审稿稿本相同。"
      }}
    </p>
    <p v-else-if="hash !== target.reviewed_hash" role="status">
      {{
        ledgerEvidence
          ? "当前已保存稿与账本来源版本不同；引文仍出现也不代表原记录仍然准确。"
          : proseEvidence
            ? "当前已保存稿与查找来源版本不同；原命中位置已停用，请按当前文字重新核对。"
            : "当前已保存稿与原审稿版本不同；引文仍出现也不代表原问题已解决。"
      }}
    </p>
    <p v-else>
      {{
        ledgerEvidence
          ? "当前已保存稿与账本来源章节版本一致；记录的解释仍需人工核对。"
          : proseEvidence
            ? "当前已保存稿与查找来源章节版本一致；字面命中范围不代表语义影响范围。"
            : "当前已保存稿与原审稿章节版本一致；修改后仍需返回报告核对。"
      }}
    </p>
    <p v-if="!matches.total" role="status">
      {{
        ledgerEvidence
          ? "当前稿未找到这段账本原引用，请结合记录手动核对。引用消失不能作为记录已经修正的依据。"
          : proseEvidence
            ? "当前稿未找到这段查找原文，请手动核对或返回正文查找重新读取；命中消失不代表相关语义已全部修改。"
            : "当前稿未找到这段原引用，请结合建议手动核对。引用消失不能作为问题已经解决的依据。"
      }}
    </p>
    <template v-else-if="pinnedPosition">
      <p>已核对原始位置与当前已保存正文，定位使用本次选中的命中。</p>
      <button :disabled="disabled" @click="locate(pinnedPosition)">
        定位本次命中
      </button>
    </template>
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
    <p v-if="hasPinnedPosition && !pinnedPosition" role="status">
      原命中位置未通过当前版本与原文核对，已按当前文字重新查找；有多处相同原文时请明确选择。
    </p>
    <button :disabled="disabled" @click="refine">带入原地精修要求</button>
    <p>
      {{
        ledgerEvidence
          ? "此操作只补充精修要求，不开始生成。正文与账本仍需分别保存、核对和独立审查；不会自动修改账本。"
          : proseEvidence
            ? "此操作只补充精修要求，不开始生成。保存后返回正文查找重新读取；不会自动全书替换、修改记忆或通过审稿。"
            : "此操作只补充精修要求，不开始生成。保存后返回同一审稿报告记录处理和人工复核；原报告始终保留。"
      }}
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
