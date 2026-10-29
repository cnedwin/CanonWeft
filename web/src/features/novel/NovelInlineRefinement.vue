<script setup lang="ts">
import { ref, watch } from "vue";
import type { RefinementMode, RefinementScope } from "./authorWorkbench";
const props = defineProps<{
  text: string;
  selection: RefinementScope | null;
  disabled: boolean;
}>();
const emit = defineEmits<{
  refine: [
    scope: RefinementScope | null,
    mode: RefinementMode,
    instruction: string,
  ];
}>();
const target = ref<"selection" | "chapter">("chapter");
const instruction = ref("");
function addInstruction(value: string) {
  if (props.disabled) return false;
  if (instruction.value.includes(value)) return true;
  const next = [instruction.value.trim(), value.trim()]
    .filter(Boolean)
    .join("\n\n");
  if ([...next].length > 4000) return false;
  instruction.value = next;
  return true;
}
defineExpose({ addInstruction });
watch(
  () => props.selection,
  (value) => {
    target.value = value ? "selection" : "chapter";
  },
);
const modes: { id: RefinementMode; label: string }[] = [
  { id: "polish", label: "润色" },
  { id: "shorten", label: "精简" },
  { id: "expand", label: "扩写" },
  { id: "custom", label: "按要求改" },
];
function request(mode: RefinementMode) {
  if (
    props.disabled ||
    !props.text.trim() ||
    (target.value === "selection" && !props.selection)
  )
    return;
  emit(
    "refine",
    target.value === "selection" ? props.selection : null,
    mode,
    instruction.value,
  );
}
</script>
<template>
  <section class="panel inline-refinement" aria-label="原地精修">
    <h2>原地精修</h2>
    <p>
      先选范围，再比较候选。正文会先暂存；生成仍需保存请求、核对费用并明确执行。
    </p>
    <label
      >精修范围<select v-model="target" :disabled="disabled">
        <option value="chapter">当前整章</option>
        <option value="selection" :disabled="!selection">
          当前选段{{
            selection
              ? ` · ${[...selection.quote].length}字`
              : " · 先在正文中选择"
          }}
        </option>
      </select></label
    >
    <blockquote
      v-if="target === 'selection' && selection"
      aria-label="待精修原文"
    >
      {{ selection.quote }}
    </blockquote>
    <label
      >这次希望怎样修改<textarea
        v-model="instruction"
        :disabled="disabled"
        rows="2"
        maxlength="4000"
        placeholder="例如：对白更克制，保留结尾的伏笔"
      />
    </label>
    <div class="refinement-actions">
      <button
        v-for="mode in modes"
        :key="mode.id"
        :disabled="
          disabled ||
          !text.trim() ||
          (mode.id === 'custom' && !instruction.trim())
        "
        @click="request(mode.id)"
      >
        {{ mode.label }}{{ target === "selection" ? "选段" : "本章" }}
      </button>
    </div>
    <p class="muted">
      精修不会直接发布。正文有新修改时，旧候选失效；选区之外被改动的候选不能采用。
    </p>
  </section>
</template>
<style scoped>
.inline-refinement {
  display: grid;
  gap: 0.75rem;
}
.inline-refinement h2,
.inline-refinement p {
  margin: 0;
}
label {
  display: grid;
  gap: 0.4rem;
}
.refinement-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
blockquote {
  white-space: pre-wrap;
  margin: 0;
  padding: 0.75rem;
  max-height: 10rem;
  overflow: auto;
  border-left: 3px solid #c7af83;
}
</style>
