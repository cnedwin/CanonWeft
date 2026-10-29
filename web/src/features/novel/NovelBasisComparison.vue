<script setup lang="ts">
import { computed } from "vue";
import PlanningReadView from "./PlanningReadView.vue";
import {
  basisValue,
  detailDifferences,
  type ChapterBasisPreview,
} from "./chapterBasis";

const props = defineProps<{
  preview: ChapterBasisPreview | null;
  busy: boolean;
  error: string;
  disabled: boolean;
}>();
const emit = defineEmits<{ retry: []; confirm: []; close: [] }>();
const differences = computed(() =>
  props.preview
    ? detailDifferences(props.preview.before.detail, props.preview.after.detail)
    : [],
);
</script>

<template>
  <section class="panel basis-comparison" aria-label="写作依据变更对照">
    <header>
      <div>
        <h2>先核对写作依据的变化</h2>
        <p>
          比较本章已保存稿所引用的旧依据与当前已确认依据。这里只读核对；正文需要作者另行修改。
        </p>
      </div>
      <button :disabled="disabled" @click="emit('close')">收起依据对照</button>
    </header>
    <p v-if="busy" role="status">正在读取本章依据变化…</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <button :disabled="busy || disabled" @click="emit('retry')">
      重新读取依据对照
    </button>
    <template v-if="preview">
      <p>
        本章保存版本 {{ preview.revision }} · 小说设置第
        {{ preview.before.settings_revision }} 版 → 第
        {{ preview.after.settings_revision }} 版
      </p>
      <p v-if="preview.reason" role="status">{{ preview.reason }}</p>
      <section v-if="preview.limitations.length" aria-label="依据对照限制">
        <h3>需要人工核对的范围</h3>
        <ul>
          <li v-for="(limit, index) in preview.limitations" :key="index">
            {{ limit }}
          </li>
        </ul>
        <p>未取得旧证据的范围不能视为没有变化。</p>
      </section>
      <h3>章节细纲变化</h3>
      <p v-if="!preview.before.detail || !preview.after.detail">
        未能取得两侧完整细纲，不能判断细纲内容是否相同。
      </p>
      <p v-else-if="!differences.length">
        可比较的细纲文字与场景安排未发现变化；引用版本或上游依据仍可能变化。
      </p>
      <article
        v-for="(difference, index) in differences"
        :key="index"
        class="basis-difference"
      >
        <h4>{{ difference.label }}</h4>
        <div class="basis-columns">
          <div>
            <strong>原依据</strong>
            <pre>{{ difference.before }}</pre>
          </div>
          <div>
            <strong>当前依据</strong>
            <pre>{{ difference.after }}</pre>
          </div>
        </div>
      </article>
      <details v-if="preview.before.detail || preview.after.detail">
        <summary>查看两侧完整细纲</summary>
        <div class="basis-columns">
          <article>
            <h3>原细纲</h3>
            <PlanningReadView
              v-if="preview.before.detail"
              :document="preview.before.detail"
            />
            <p v-else>原细纲不可用</p>
          </article>
          <article>
            <h3>当前细纲</h3>
            <PlanningReadView
              v-if="preview.after.detail"
              :document="preview.after.detail"
            />
            <p v-else>当前细纲不可用</p>
          </article>
        </div>
      </details>
      <h3>小说设置变化</h3>
      <p v-if="!preview.settings_changes.length">
        可比较范围未列出设置字段变化；证据不足部分以上述限制为准。
      </p>
      <article
        v-for="change in preview.settings_changes"
        :key="change.field"
        class="basis-difference"
      >
        <h4>{{ change.label }}</h4>
        <div class="basis-columns">
          <div>
            <strong>原依据</strong>
            <pre>{{ basisValue(change.before) }}</pre>
          </div>
          <div>
            <strong>当前依据</strong>
            <pre>{{ basisValue(change.after) }}</pre>
          </div>
        </div>
      </article>
      <h3>人物与世界设定变化</h3>
      <p v-if="!preview.ip_changes.length">
        可比较范围未列出设定字段变化；不代表所有语义影响已被自动识别。
      </p>
      <section v-for="(change, index) in preview.ip_changes" :key="index">
        <h4>{{ change.name || change.ref.id }}</h4>
        <article
          v-for="(field, fieldIndex) in change.fields"
          :key="fieldIndex"
          class="basis-difference"
        >
          <h5>{{ field.name }}</h5>
          <div class="basis-columns">
            <div>
              <strong>原依据</strong>
              <pre>{{ basisValue(field.before) }}</pre>
            </div>
            <div>
              <strong>当前依据</strong>
              <pre>{{ basisValue(field.after) }}</pre>
            </div>
          </div>
        </article>
      </section>
      <p>
        确认只更新本章引用的细纲、设置与设定版本，保留正文、标题、引文和故事位置；不会自动润色、采用正文或调用模型。随后可继续手改，或从原地精修入口准备修改。
      </p>
      <button
        :disabled="disabled || busy || !preview.can_refresh"
        @click="emit('confirm')"
      >
        确认更新依据，正文保持原样
      </button>
    </template>
  </section>
</template>

<style scoped>
.basis-comparison {
  display: grid;
  gap: 1rem;
}
header {
  display: flex;
  gap: 1rem;
  align-items: flex-start;
  justify-content: space-between;
  flex-wrap: wrap;
}
h2,
h3,
h4,
h5,
p {
  margin: 0.35rem 0;
}
.basis-columns {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}
.basis-difference {
  padding: 0.75rem;
  border: 1px solid #dce1e7;
  border-radius: 0.4rem;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: inherit;
  max-height: 24rem;
  overflow: auto;
}
@media (max-width: 760px) {
  .basis-columns {
    grid-template-columns: 1fr;
  }
}
</style>
