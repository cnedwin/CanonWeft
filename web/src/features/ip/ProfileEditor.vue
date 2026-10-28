<script setup lang="ts">
import { watch } from "vue";
import type { Entity, CapabilityProfile } from "./types";
const props = defineProps<{ entity: Entity; entities: Entity[] }>();
const emit = defineEmits<{ edited: [] }>();
const fields: {
  key: "effects" | "costs" | "limits" | "counters" | "growth";
  label: string;
}[] = [
  { key: "effects", label: "效果或职业职能" },
  { key: "costs", label: "代价" },
  { key: "limits", label: "限制" },
  { key: "counters", label: "反制方式" },
  { key: "growth", label: "成长路径" },
];
watch(
  () => props.entity.kind,
  (kind) => {
    if (
      ["ability", "profession", "technique", "cultivation"].includes(kind) &&
      !props.entity.profile
    ) {
      props.entity.profile = {
        requirements: [],
        effects: [],
        costs: [],
        limits: [],
        counters: [],
        growth: [],
        related: [],
        source: "作者输入",
      };
      emit("edited");
    }
  },
  { immediate: true },
);
function lines(
  key: keyof Pick<
    CapabilityProfile,
    "effects" | "costs" | "limits" | "counters" | "growth"
  >,
  event: Event,
) {
  const value = (event.target as HTMLTextAreaElement).value;
  if (props.entity.profile)
    props.entity.profile[key] = value === "" ? [] : value.split(/\r?\n/);
}
function add() {
  props.entity.profile?.requirements.push({
    subject: props.entities.find((e) => e.id !== props.entity.id)?.id ?? "",
    predicate: "",
    operator: "equals",
    operand: "",
  });
  emit("edited");
}
</script>
<template>
  <section v-if="entity.profile" class="access">
    <h4>结构化定义</h4>
    <p class="muted">
      共享的是条件、效果和限制；具体游戏伤害、按键等由作品实现单独定义。
    </p>
    <label v-for="field in fields" :key="field.key"
      >{{ field.label }}（每行一项）<textarea
        :aria-label="field.label"
        :value="entity.profile[field.key]?.join('\n')"
        :required="field.key === 'effects'"
        rows="2"
        @input="lines(field.key, $event)"
      ></textarea>
    </label>
    <label>定义来源<input v-model="entity.profile.source" required /></label>
    <label
      >关联职业、功法或实体<select
        v-model="entity.profile.related"
        multiple
        aria-label="关联职业、功法或实体"
      >
        <option
          v-for="e in entities.filter((e) => e.id !== entity.id)"
          :key="e.id"
          :value="e.id"
        >
          {{ e.name }}
        </option>
      </select></label
    >
    <h4>发动或准入条件</h4>
    <p class="muted">
      没有条件表示无已声明的前置条件；有条件但缺少事实证据时为“未知”。
    </p>
    <div
      v-for="(condition, i) in entity.profile.requirements"
      :key="i"
      class="field-grid"
    >
      <label
        >条件主体<select
          v-model="condition.subject"
          required
          aria-label="条件主体"
        >
          <option v-for="e in entities" :key="e.id" :value="e.id">
            {{ e.name }}
          </option>
        </select></label
      >
      <label>条件属性<input v-model="condition.predicate" required /></label>
      <label
        >条件判断<select v-model="condition.operator" aria-label="条件判断">
          <option value="equals">等于</option>
          <option value="not_equals">不等于</option>
          <option value="number_lte">不高于</option>
          <option value="number_gte">不低于</option>
        </select></label
      >
      <label>条件值<input v-model="condition.operand" required /></label>
      <button
        type="button"
        @click="
          entity.profile.requirements.splice(i, 1);
          emit('edited');
        "
      >
        移除此条件
      </button>
    </div>
    <button type="button" @click="add">添加发动条件</button>
  </section>
</template>
