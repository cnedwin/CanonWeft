<script setup lang="ts">
import type { AuthorIntent } from "./authorWorkbench";
defineProps<{
  chapterActive?: boolean;
  toolActive?: boolean;
  disabled?: boolean;
}>();
const emit = defineEmits<{ intent: [intent: AuthorIntent] }>();
const actions: { intent: AuthorIntent; label: string }[] = [
  { intent: "planning", label: "理清规划" },
  { intent: "characters", label: "完善人物" },
  { intent: "materials", label: "查资料" },
  { intent: "write", label: "写一章" },
  { intent: "batch", label: "连续写章" },
  { intent: "refine", label: "精修正文" },
];
</script>
<template>
  <aside class="panel author-assistant" aria-label="创作助手">
    <div>
      <strong>创作助手</strong>
      <p class="muted">
        {{
          toolActive
            ? "当前工具的输入会留在本书。完成或暂存后返回，即可切换下一项工作。"
            : chapterActive
              ? "当前章可直接写作或精修；切换其他工具前请保存返回。"
              : "选择这次要做的事。写作与精修先明确章节，生成沿用费用估算与候选确认。"
        }}
      </p>
    </div>
    <div class="assistant-actions">
      <button
        v-for="action in actions"
        :key="action.intent"
        class="quiet"
        :disabled="
          disabled ||
          (chapterActive && !['write', 'refine'].includes(action.intent))
        "
        @click="emit('intent', action.intent)"
      >
        {{ action.label }}
      </button>
    </div>
  </aside>
</template>
<style scoped>
.author-assistant {
  display: flex;
  gap: 1rem;
  justify-content: space-between;
  align-items: center;
  margin: 1rem 0;
}
.author-assistant p {
  margin: 0.4rem 0 0;
  max-width: 32rem;
}
.assistant-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
@media (max-width: 760px) {
  .author-assistant {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
