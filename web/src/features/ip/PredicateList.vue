<script setup lang="ts">
import type { Entity, PredicateTest } from "./types";
const props = defineProps<{
  modelValue?: PredicateTest[];
  entities: Entity[];
  label: string;
}>();
const emit = defineEmits<{
  "update:modelValue": [PredicateTest[]];
  edited: [];
}>();
function add() {
  emit("update:modelValue", [
    ...(props.modelValue ?? []),
    {
      subject: props.entities[0]?.id ?? "",
      predicate: "",
      operator: "equals",
      operand: "",
    },
  ]);
  emit("edited");
}
function remove(index: number) {
  emit(
    "update:modelValue",
    (props.modelValue ?? []).filter((_, i) => i !== index),
  );
  emit("edited");
}
</script>
<template>
  <div class="predicate-list" @input="emit('edited')" @change="emit('edited')">
    <p>{{ label }}（全部成立才满足）</p>
    <div v-for="(item, index) in modelValue" :key="index" class="predicate-row">
      <label
        >条件主体<select v-model="item.subject" aria-label="条件主体" required>
          <option
            v-for="entity in entities"
            :key="entity.id"
            :value="entity.id"
          >
            {{ entity.name }}
          </option>
        </select></label
      ><label>条件属性<input v-model="item.predicate" required /></label
      ><label
        >条件运算<select v-model="item.operator" aria-label="条件运算">
          <option value="equals">等于</option>
          <option value="not_equals">不等于</option>
          <option value="number_lte">不高于</option>
          <option value="number_gte">不低于</option>
        </select></label
      ><label>条件值<input v-model="item.operand" required /></label
      ><button type="button" class="quiet" @click="remove(index)">
        移除条件
      </button>
    </div>
    <button
      type="button"
      class="quiet"
      :disabled="!entities.length"
      @click="add"
    >
      添加{{ label }}
    </button>
  </div>
</template>
<style scoped>
.predicate-list {
  padding: 0.6rem 0;
}
.predicate-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 0.6rem;
  margin-bottom: 0.5rem;
}
</style>
