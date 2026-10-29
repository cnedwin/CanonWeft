<script setup lang="ts">
import { computed, ref } from "vue";
import {
  editorialFindings,
  editorialPlatforms,
  editorialPrompt,
  editorialSources,
} from "./editorial";
const props = defineProps<{
  text: string;
  quote?: string;
  disabled: boolean;
}>();
const emit = defineEmits<{ assist: [prompt: string] }>();
const platform = ref("general"),
  audience = ref("不限"),
  focus = ref("");
const findings = computed(() => editorialFindings(props.text));
defineExpose({ hasInput: () => !!focus.value.trim() });
</script>
<template>
  <section class="panel editorial-desk" aria-label="自然文风与投稿前自检">
    <h2>自然文风与投稿前自检</h2>
    <p>
      先检查具体段落，再用助手精修。以下提示不判断“是否AI写作”，也不代表签约或人工审核结果。
    </p>
    <div class="editorial-fields">
      <label
        >目标平台<select
          v-model="platform"
          aria-label="目标平台"
          :disabled="disabled"
        >
          <option v-for="p in editorialPlatforms" :key="p.id" :value="p.id">
            {{ p.name }}
          </option>
        </select></label
      >
      <label
        >目标读者<select
          v-model="audience"
          aria-label="目标读者"
          :disabled="disabled"
        >
          <option>不限</option>
          <option>男频</option>
          <option>女频</option>
        </select></label
      >
      <label
        >题材与编辑重点<input
          v-model="focus"
          maxlength="1000"
          :disabled="disabled"
          placeholder="例如：悬疑群像，线索公平，人物说话有区别"
      /></label>
    </div>
    <p class="muted">
      男频与女频仅调整检查侧重，具体题材优先；没有统一的“潜规则”或可靠的过审分数。
    </p>
    <p v-if="quote">已选中 {{ [...quote].length }} 字，将优先精修选段。</p>
    <button
      :disabled="disabled || !text.trim()"
      @click="emit('assist', editorialPrompt(platform, audience, focus, quote))"
    >
      {{ quote ? "让助手精修选段" : "让助手自然文风精修" }}
    </button>
    <ul v-if="findings.length">
      <li v-for="(f, i) in findings" :key="i">
        <strong>{{ f.label }}</strong>
        <blockquote>{{ f.evidence }}</blockquote>
        {{ f.advice }}
      </li>
    </ul>
    <p v-else class="muted">
      未发现本地规则覆盖的明显重复或超长段落；人物、逻辑、事实与文学质量仍需审阅。
    </p>
    <details>
      <summary>规则依据与适用边界 · 核查于2026-10-27</summary>
      <p>
        官方要求与编辑建议分开使用；未核实的“改写比例”“AI分数门槛”不作为规则。请核对实际站点、合同及投稿时的新要求。
      </p>
      <ul>
        <li v-for="source in editorialSources" :key="source.url">
          <a :href="source.url" target="_blank" rel="noopener noreferrer">{{
            source.name
          }}</a>
        </li>
      </ul>
    </details>
  </section>
</template>
<style scoped>
.editorial-fields {
  display: grid;
  grid-template-columns: 1fr 1fr 2fr;
  gap: 12px;
}
label {
  display: grid;
  gap: 6px;
}
blockquote {
  white-space: pre-wrap;
  margin: 8px 0;
  padding: 8px;
  border-left: 3px solid #c7af83;
}
@media (max-width: 750px) {
  .editorial-fields {
    grid-template-columns: 1fr;
  }
}
</style>
