<script setup lang="ts">
import type { Snapshot } from "./types";
import RuleScopeEditor from "./RuleScopeEditor.vue";
const props = defineProps<{
  snapshot: Snapshot;
  published: Snapshot;
  busy: boolean;
  amendment: boolean;
}>();
const emit = defineEmits<{ edited: [] }>();
function addRule() {
  (props.snapshot.rules ??= []).push({
    id: crypto.randomUUID(),
    name: "",
    subject: props.snapshot.entities[0]?.id ?? "",
    predicate: "",
    operator: "equals",
    operand: "",
    source: "作者输入",
    locked: false,
    recorded_at: "",
  });
  emit("edited");
}
function addEvent() {
  (props.snapshot.events ??= []).push({
    id: crypto.randomUUID(),
    name: "",
    description: "",
    time: 0,
    participants: [],
    causes: [],
    source: "作者输入",
    locked: false,
    recorded_at: "",
  });
  emit("edited");
}
</script>
<template>
  <h3>世界规则</h3>
  <p class="muted">
    规则关联具体实体与属性，用于检查事实是否超出世界设定。缺少证据时结果为“未知”。
  </p>
  <fieldset
    v-for="(r, index) in snapshot.rules"
    :key="r.id"
    class="entity-editor"
    :disabled="
      busy ||
      (!amendment &&
        !!published.rules?.find((old) => old.id === r.id && old.locked))
    "
  >
    <legend>规则 {{ index + 1 }}</legend>
    <div class="field-grid">
      <label>规则名称<input v-model="r.name" required /></label>
      <label
        >约束主体<select v-model="r.subject" required>
          <option v-for="e in snapshot.entities" :key="e.id" :value="e.id">
            {{ e.name }}
          </option>
        </select></label
      >
      <label>约束属性<input v-model="r.predicate" required /></label>
      <label
        >运算<select v-model="r.operator">
          <option value="equals">必须等于</option>
          <option value="not_equals">不得等于</option>
          <option value="number_lte">数值不得高于</option>
          <option value="number_gte">数值不得低于</option>
        </select></label
      >
      <label>约束值<input v-model="r.operand" required /></label
      ><label>规则来源<input v-model="r.source" required /></label>
    </div>
    <label class="checkbox-label"
      ><input v-model="r.locked" type="checkbox" />锁定规则</label
    >
    <RuleScopeEditor
      :rule="r"
      :entities="snapshot.entities"
      @edited="emit('edited')"
    />
    <button
      type="button"
      class="quiet"
      @click="
        snapshot.rules?.splice(index, 1);
        emit('edited');
      "
    >
      移除此规则
    </button>
  </fieldset>
  <button
    type="button"
    class="quiet"
    :disabled="busy || !snapshot.entities.length"
    @click="addRule"
  >
    新增世界规则
  </button>
  <h3>故事事件与因果</h3>
  <fieldset
    v-for="(e, index) in snapshot.events"
    :key="e.id"
    class="entity-editor"
    :disabled="
      busy ||
      (!amendment &&
        !!published.events?.find((old) => old.id === e.id && old.locked))
    "
  >
    <legend>事件 {{ index + 1 }}</legend>
    <div class="field-grid">
      <label>事件名称<input v-model="e.name" required /></label
      ><label
        >发生时间<input
          v-model.number="e.time"
          type="number"
          step="1"
          required /></label
      ><label
        >参与者<select v-model="e.participants" multiple>
          <option
            v-for="entity in snapshot.entities"
            :key="entity.id"
            :value="entity.id"
          >
            {{ entity.name }}
          </option>
        </select></label
      ><label
        >前置原因<select v-model="e.causes" multiple>
          <option
            v-for="cause in snapshot.events?.filter((c) => c.id !== e.id)"
            :key="cause.id"
            :value="cause.id"
          >
            {{ cause.name }}
          </option>
        </select></label
      >
    </div>
    <label>事件描述<textarea v-model="e.description" rows="3"></textarea></label
    ><label>事件来源<input v-model="e.source" required /></label>
    <label class="checkbox-label"
      ><input v-model="e.locked" type="checkbox" />锁定事件</label
    ><button
      type="button"
      class="quiet"
      @click="
        snapshot.events?.splice(index, 1);
        emit('edited');
      "
    >
      移除此事件
    </button>
  </fieldset>
  <button type="button" class="quiet" :disabled="busy" @click="addEvent">
    新增故事事件
  </button>
</template>
