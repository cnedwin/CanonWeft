<script setup lang="ts">
import {
  planFields,
  chapterFields,
  sceneFields,
  type PlanningDocument,
} from "./planningTypes";
defineProps<{ document: PlanningDocument }>();
</script>
<template>
  <div class="planning-read-view">
    <p>
      依据小说设置第{{ document.settings_revision }}版 · 引用{{
        document.inputs?.length ?? 0
      }}份已确认资料与规划
    </p>
    <dl>
      <template
        v-for="field in planFields.filter((f) =>
          f.kinds.includes(document.kind),
        )"
        :key="field.key"
      >
        <dt>{{ field.label }}</dt>
        <dd>{{ document[field.key] || "未指定" }}</dd>
      </template>
    </dl>
    <section v-for="(volume, index) in document.volumes" :key="volume.id">
      <h4>第{{ index + 1 }}卷：{{ volume.title }}</h4>
      <p>{{ volume.summary }}</p>
    </section>
    <section v-for="(chapter, index) in document.chapters" :key="chapter.id">
      <h4>第{{ index + 1 }}章：{{ chapter.title }}</h4>
      <dl>
        <template v-for="field in chapterFields" :key="field.key"
          ><dt>{{ field.label }}</dt>
          <dd>{{ chapter[field.key] }}</dd></template
        >
      </dl>
    </section>
    <section v-for="(scene, index) in document.scenes" :key="scene.id">
      <h4>场景{{ index + 1 }} · {{ scene.words }}字</h4>
      <dl>
        <template v-for="field in sceneFields" :key="field.key"
          ><dt>{{ field.label }}</dt>
          <dd>{{ scene[field.key] || "未指定" }}</dd></template
        >
      </dl>
    </section>
  </div>
</template>
<style scoped>
dl {
  display: grid;
  grid-template-columns: minmax(100px, 180px) 1fr;
  gap: 10px;
}
dt {
  font-weight: 600;
}
dd {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
section {
  border-top: 1px solid #dce1e7;
  padding: 12px 0;
}
@media (max-width: 600px) {
  dl {
    grid-template-columns: 1fr;
  }
}
</style>
