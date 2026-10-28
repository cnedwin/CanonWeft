<script setup lang="ts">
import type { Entity } from "./types";
import { spaceNames, type KnowledgePolicy } from "./knowledgePolicy";
const props = defineProps<{
  policy: KnowledgePolicy;
  entities: Entity[];
  disabled: boolean;
  allowWithdrawal?: boolean;
}>();
const emit = defineEmits<{ changed: [] }>();
function spaceChanged() {
  props.policy.visibility = "author";
  props.policy.known_by = [];
  emit("changed");
}
function withdrawalChanged() {
  if (!props.policy.withdrawn) {
    delete props.policy.withdrawn;
    delete props.policy.withdrawal_reason;
  }
  emit("changed");
}
</script>
<template>
  <fieldset :disabled="disabled" class="source-policy">
    <legend>资料用途与知情范围</legend>
    <template v-if="allowWithdrawal || policy.withdrawn">
      <label class="withdrawal-choice"
        ><input
          v-model="policy.withdrawn"
          type="checkbox"
          @change="withdrawalChanged"
        />撤回这份资料的使用</label
      >
      <label v-if="policy.withdrawn"
        >撤回原因<textarea
          v-model="policy.withdrawal_reason"
          maxlength="2000"
          rows="2"
          @input="emit('changed')"
        />
      </label>
      <p v-if="policy.withdrawn" class="muted">
        确认发布后停止进入新的检索和生成，保留历史原文及引用。受影响的作品需要复核，不自动改写。取消勾选须作为新版本重新确认发布。
      </p>
    </template>
    <label
      >知识空间<select
        v-model="policy.space"
        aria-label="知识空间"
        @change="spaceChanged"
      >
        <option v-for="(name, key) in spaceNames" :key="key" :value="key">
          {{ name }}
        </option>
      </select></label
    >
    <p v-if="policy.space === 'methods'" class="muted">
      这里保存方法参考，不能代替能力中心的 Skill 评测和执行资格。
    </p>
    <p v-if="policy.space === 'research'" class="muted">
      只供研究用途检索；保存与发布本资料都不会激活其中的指令或脚本。
    </p>
    <p v-if="policy.space === 'working'" class="muted">
      规划、制作笔记和待办只在任务明确选择工作资料时纳入，不成为剧情事实。
    </p>
    <template v-if="policy.space === 'evidence'">
      <label
        >原文知情范围<select
          aria-label="原文知情范围"
          v-model="policy.visibility"
          @change="emit('changed')"
        >
          <option value="author">仅作者与审查</option>
          <option value="public">故事内公开</option>
          <option value="restricted">指定角色知情</option>
        </select></label
      >
      <label v-if="policy.visibility === 'restricted'"
        >知情角色（可多选）<select
          aria-label="知情角色（可多选）"
          v-model="policy.known_by"
          multiple
          @change="emit('changed')"
        >
          <option
            v-for="entity in entities"
            :key="entity.id"
            :value="entity.id"
          >
            {{ entity.name }}
          </option>
        </select></label
      >
      <label v-if="policy.visibility !== 'author'"
        >原文最早可知时间<input
          v-model.number="policy.reveal_at"
          type="number"
          @input="emit('changed')"
      /></label>
    </template>
    <small
      >此标注与正文一起成为候选，确认发布后生效；修改会令旧证据包过期。</small
    >
  </fieldset>
</template>
<style scoped>
.source-policy {
  margin: 1rem 0;
  display: grid;
  gap: 0.8rem;
}
select[multiple] {
  min-height: 5rem;
}
.withdrawal-choice {
  display: flex;
  gap: 0.6rem;
  align-items: center;
}
.withdrawal-choice input {
  width: auto;
}
</style>
