<script setup lang="ts">
import type { Snapshot } from "./types";
const props = defineProps<{ snapshot: Snapshot; busy: boolean }>();
const emit = defineEmits<{ edited: [] }>();
function add() {
  (props.snapshot.mappings ??= []).push({
    id: crypto.randomUUID(),
    kind: "composite_of",
    target: "",
    sources: [],
    reason: "",
    source: "改编计划",
  });
  emit("edited");
}
</script>
<template>
  <section>
    <h3>改编身份映射</h3>
    <p class="muted">
      组合、拆分或代表角色使用独立身份。此关联不会继承来源角色的秘密、信念或知情范围。
    </p>
    <fieldset
      v-for="(m, i) in snapshot.mappings"
      :key="m.id"
      :disabled="busy"
      class="entity-editor"
    >
      <legend>身份映射 {{ i + 1 }}</legend>
      <label
        >映射类型<select v-model="m.kind" aria-label="映射类型">
          <option value="composite_of">组合自多个角色</option>
          <option value="split_from">拆分自原角色</option>
          <option value="represents">代表或表现</option>
        </select></label
      >
      <label
        >改编实体<select v-model="m.target" required aria-label="改编实体">
          <option value="">请选择</option>
          <option v-for="e in snapshot.entities" :key="e.id" :value="e.id">
            {{ e.name }}
          </option>
        </select></label
      >
      <label
        >来源实体<select
          v-model="m.sources"
          required
          multiple
          aria-label="来源实体"
        >
          <option
            v-for="e in snapshot.entities.filter((e) => e.id !== m.target)"
            :key="e.id"
            :value="e.id"
          >
            {{ e.name }}
          </option>
        </select></label
      >
      <label>映射理由<input v-model="m.reason" required /></label
      ><label>映射来源<input v-model="m.source" required /></label>
      <button
        type="button"
        @click="
          snapshot.mappings?.splice(i, 1);
          emit('edited');
        "
      >
        移除此映射
      </button>
    </fieldset>
    <button
      type="button"
      :disabled="busy || snapshot.entities.length < 2"
      @click="add"
    >
      添加身份映射
    </button>
  </section>
</template>
