<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { ImageRectangle, ImageRegion } from "./imageRegions";
const props = defineProps<{
  src: string;
  modelValue: ImageRegion;
  readonly?: boolean;
}>();
const emit = defineEmits<{ "update:modelValue": [value: ImageRegion] }>();
const loaded = ref(false),
  error = ref("");
const start = ref<{ x: number; y: number }>(),
  drawing = ref<ImageRectangle>();
watch(
  () => props.src,
  () => {
    loaded.value = false;
    error.value = "";
    start.value = undefined;
    drawing.value = undefined;
  },
);
const shown = computed(() => [
  ...props.modelValue.areas,
  ...(drawing.value ? [drawing.value] : []),
]);
function update(areas: ImageRectangle[], preserve = props.modelValue.preserve) {
  if (!props.readonly)
    emit("update:modelValue", { ...props.modelValue, areas, preserve });
}
function point(e: PointerEvent) {
  const b = (e.currentTarget as HTMLElement).getBoundingClientRect();
  return {
    x: Math.max(
      0,
      Math.min(10000, Math.round(((e.clientX - b.left) / b.width) * 10000)),
    ),
    y: Math.max(
      0,
      Math.min(10000, Math.round(((e.clientY - b.top) / b.height) * 10000)),
    ),
  };
}
function down(e: PointerEvent) {
  if (
    props.readonly ||
    !loaded.value ||
    props.modelValue.areas.length >= 16 ||
    e.button !== 0
  )
    return;
  start.value = point(e);
  drawing.value = undefined;
  (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
}
function move(e: PointerEvent) {
  if (!start.value) return;
  const p = point(e),
    s = start.value;
  drawing.value = {
    x: Math.min(s.x, p.x),
    y: Math.min(s.y, p.y),
    width: Math.abs(p.x - s.x),
    height: Math.abs(p.y - s.y),
  };
}
function up(e: PointerEvent) {
  if (!start.value) return;
  move(e);
  if (drawing.value && drawing.value.width > 0 && drawing.value.height > 0)
    update([...props.modelValue.areas, { ...drawing.value }]);
  start.value = undefined;
  drawing.value = undefined;
}
function ready(e: Event) {
  const i = e.target as HTMLImageElement;
  loaded.value =
    i.naturalWidth === props.modelValue.width &&
    i.naturalHeight === props.modelValue.height;
  if (!loaded.value) error.value = "原图尺寸与记录不一致，请重新选择原图。";
}
function dimension(i: number, key: keyof ImageRectangle, event: Event) {
  const n = Number((event.target as HTMLInputElement).value);
  if (!Number.isFinite(n) || n < 0 || n > 100) return;
  const areas = props.modelValue.areas.map((a) => ({ ...a }));
  const a = areas[i];
  if (!a) return;
  a[key] = Math.round(n * 100);
  if (
    a.width <= 0 ||
    a.height <= 0 ||
    a.x + a.width > 10000 ||
    a.y + a.height > 10000
  ) {
    error.value = "选区必须在图片内，并且宽、高大于零。";
    return;
  }
  error.value = "";
  update(areas);
}
</script>
<template>
  <section aria-label="局部修改区域" class="region-editor">
    <p>
      原图 {{ modelValue.width }} × {{ modelValue.height }} ·
      {{ modelValue.areas.length }} 个选区
    </p>
    <p v-if="!readonly">
      在图上拖动框选需要修改的部位，可添加最多16个区域，也可用下方数值调整。区域之外及保留特征仍需在生成后核对；模型不保证像素完全不变。
    </p>
    <div
      class="region-stage"
      data-testid="region-stage"
      :class="{ editable: !readonly }"
      @pointerdown="down"
      @pointermove="move"
      @pointerup="up"
      @pointercancel="
        start = undefined;
        drawing = undefined;
      "
    >
      <img
        :src="src"
        alt="局部修改原图"
        draggable="false"
        @load="ready"
        @error="error = '原图读取失败，请检查图片是否仍可访问。'"
      />
      <svg
        viewBox="0 0 10000 10000"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <rect
          v-for="(r, i) in shown"
          :key="i"
          :x="r.x"
          :y="r.y"
          :width="r.width"
          :height="r.height"
        />
      </svg>
    </div>
    <a :href="src" target="_blank" rel="noopener">放大查看原图</a>
    <template v-if="!readonly">
      <button
        type="button"
        :disabled="!loaded || modelValue.areas.length >= 16"
        @click="
          update([
            ...modelValue.areas,
            { x: 2500, y: 2500, width: 5000, height: 5000 },
          ])
        "
      >
        添加中央选区
      </button>
      <button
        type="button"
        :disabled="!modelValue.areas.length"
        @click="update([])"
      >
        清除全部选区
      </button>
      <fieldset v-for="(r, i) in modelValue.areas" :key="i">
        <legend>选区 {{ i + 1 }}（占原图百分比）</legend>
        <label
          v-for="(label, key) in {
            x: '左侧位置',
            y: '顶部位置',
            width: '区域宽度',
            height: '区域高度',
          }"
          :key="key"
          >{{ label
          }}<input
            type="number"
            min="0"
            max="100"
            step="0.01"
            :value="r[key] / 100"
            @change="dimension(i, key, $event)"
        /></label>
        <button
          type="button"
          @click="update(modelValue.areas.filter((_, j) => j !== i))"
        >
          移除选区 {{ i + 1 }}
        </button>
      </fieldset>
      <label
        >必须保留的特征<textarea
          :value="modelValue.preserve"
          rows="3"
          maxlength="4000"
          @input="
            update(
              modelValue.areas,
              ($event.target as HTMLTextAreaElement).value,
            )
          "
        />
      </label>
    </template>
    <p v-else class="preserve">必须保留：{{ modelValue.preserve }}</p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
  </section>
</template>
<style scoped>
.region-editor {
  border: 1px solid var(--border, #ddd);
  padding: 1rem;
  margin: 1rem 0;
  border-radius: 8px;
}
.region-stage {
  position: relative;
  width: 100%;
  max-width: 800px;
  line-height: 0;
  touch-action: none;
  user-select: none;
}
.region-stage.editable {
  cursor: crosshair;
}
.region-stage img {
  display: block;
  width: 100%;
  height: auto;
  pointer-events: none;
}
svg {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  pointer-events: none;
}
rect {
  fill: #e1aa2260;
  stroke: #bc6400;
  stroke-width: 30;
  vector-effect: non-scaling-stroke;
  stroke-width: 2px;
}
fieldset {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin: 0.75rem 0;
}
fieldset label {
  max-width: 9rem;
}
textarea {
  width: 100%;
  box-sizing: border-box;
}
button,
a {
  margin: 0.5rem 0.5rem 0.5rem 0;
}
label {
  display: block;
}
.preserve {
  white-space: pre-wrap;
}
</style>
