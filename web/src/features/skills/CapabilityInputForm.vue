<script setup lang="ts">
import { computed } from "vue";
const props = defineProps<{
  schema: Record<string, unknown>;
  modelValue: string;
  label: string;
}>();
const emit = defineEmits<{ "update:modelValue": [string] }>();
const fields = computed(() =>
  Object.entries(
    (props.schema.properties ?? {}) as Record<
      string,
      { type?: string; title?: string }
    >,
  ),
);
const simple = computed(
  () =>
    fields.value.length > 0 &&
    fields.value.every(([, value]) => value.type === "string") &&
    !props.schema.allOf &&
    !props.schema.oneOf &&
    !props.schema.anyOf,
);
const values = computed<Record<string, string>>(() => {
  if (!simple.value) return {};
  try {
    return JSON.parse(props.modelValue) as Record<string, string>;
  } catch {
    return {};
  }
});
const names: Record<string, string> = {
  source: "资料",
  objective: "目标",
  materials: "材料与来源",
  summary: "摘要",
  feedback: "修改意见",
};
function update(key: string, event: Event) {
  emit(
    "update:modelValue",
    JSON.stringify({
      ...values.value,
      [key]: (event.target as HTMLTextAreaElement).value,
    }),
  );
}
</script>
<template>
  <fieldset>
    <legend>{{ label }}</legend>
    <template v-if="simple">
      <label v-for="[key, field] in fields" :key="key"
        >{{ label }} · {{ field.title ?? names[key] ?? key }}
        <textarea
          :value="values[key] ?? ''"
          rows="3"
          maxlength="262144"
          @input="update(key, $event)"
        />
      </label>
    </template>
    <label v-else
      >{{ label }} · 完整资料
      <textarea
        :value="modelValue"
        rows="6"
        maxlength="262144"
        spellcheck="false"
        @input="
          emit(
            'update:modelValue',
            ($event.target as HTMLTextAreaElement).value,
          )
        "
      />
      <small
        >此自定义契约需要结构化资料，按下方格式填写；数字和原文会完整送交后端检查。</small
      >
      <details>
        <summary>资料格式</summary>
        <pre>{{ JSON.stringify(schema, null, 2) }}</pre>
      </details>
    </label>
  </fieldset>
</template>
