<script setup lang="ts">
import type { Entity, WorldRule } from "./types";
import PredicateList from "./PredicateList.vue";
const props = defineProps<{ rule: WorldRule; entities: Entity[] }>();
const emit = defineEmits<{ edited: [] }>();
function time(field: "effective_from" | "effective_until", event: Event) {
  const target = event.target as HTMLInputElement;
  props.rule[field] = target.value === "" ? null : target.valueAsNumber;
  emit("edited");
}
function addException() {
  (props.rule.exceptions ??= []).push({
    id: crypto.randomUUID(),
    name: "",
    source: "作者设定",
    when: [
      {
        subject: props.entities[0]?.id ?? "",
        predicate: "",
        operator: "equals",
        operand: "",
      },
    ],
  });
  emit("edited");
}
</script>
<template>
  <details class="rule-scope">
    <summary>适用条件、有效期与例外</summary>
    <p class="muted">
      留空时不限时间且无附加条件。任一完整例外成立可豁免；条件缺证据时显示未知，不自动放行。
    </p>
    <div class="field-grid">
      <label
        >规则生效时间<input
          type="number"
          :value="rule.effective_from ?? ''"
          @input="time('effective_from', $event)" /></label
      ><label
        >规则结束时间（不含）<input
          type="number"
          :value="rule.effective_until ?? ''"
          @input="time('effective_until', $event)"
      /></label>
    </div>
    <PredicateList
      v-model="rule.when"
      :entities="entities"
      label="适用条件"
      @edited="emit('edited')"
    />
    <fieldset v-for="(exception, index) in rule.exceptions" :key="exception.id">
      <legend>例外 {{ index + 1 }}</legend>
      <label
        >例外名称<input
          v-model="exception.name"
          required
          @input="emit('edited')" /></label
      ><label
        >例外来源<input
          v-model="exception.source"
          required
          @input="emit('edited')" /></label
      ><PredicateList
        v-model="exception.when"
        :entities="entities"
        label="例外条件"
        @edited="emit('edited')"
      /><button
        type="button"
        class="quiet"
        @click="
          rule.exceptions?.splice(index, 1);
          emit('edited');
        "
      >
        移除此例外
      </button>
    </fieldset>
    <button
      type="button"
      class="quiet"
      :disabled="!entities.length"
      @click="addException"
    >
      添加明确例外
    </button>
  </details>
</template>
<style scoped>
.rule-scope {
  margin: 1rem 0;
}
fieldset {
  border: 1px solid #dcd8cd;
  border-radius: 0.5rem;
  padding: 1rem;
  margin: 1rem 0;
}
label {
  margin: 0.6rem 0;
}
</style>
