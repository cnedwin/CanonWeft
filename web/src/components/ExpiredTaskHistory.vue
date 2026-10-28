<script setup lang="ts">
import { computed } from "vue";
const props = defineProps<{
  scope: string;
  items: { id: string; retention?: { state: "EXPIRED"; expired_at: string } }[];
}>();
const expired = computed(() => props.items.filter((item) => item.retention));
</script>

<template>
  <details v-if="expired.length" class="expired-task-history">
    <summary>{{ scope }}过期任务历史（{{ expired.length }}）</summary>
    <p>
      运行材料已到保留期限，不能继续执行或取得新资格。已记录费用和未知占用仍保留。
    </p>
    <ul>
      <li v-for="item in expired" :key="item.id">
        任务 {{ item.id.slice(0, 8) }} · 过期于
        {{ new Date(item.retention!.expired_at).toLocaleString() }}
      </li>
    </ul>
  </details>
</template>
