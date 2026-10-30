<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { api, message, newOperationID } from "../../api";
import type { Actor, Project } from "../../api";
import type { Branch } from "./types";
import GenerationPanel from "./GenerationPanel.vue";
import ImageRegionEditor from "./ImageRegionEditor.vue";
import type { ImageRegion } from "./imageRegions";
import {
  visualDimensions,
  visualLabel,
  type NovelImageTarget,
  type VisualImageChoice,
} from "./visualTypes";
const props = defineProps<{
  projectId: string;
  branchId: string;
  branchHead: string;
  characterId: string;
  novel?: NovelImageTarget;
  disabled: boolean;
  images: VisualImageChoice[];
}>();
interface VisualSuggestion {
  references: { id: string; hash: string }[];
  generation_id: string;
  character_hash: string;
  proposal: {
    novel?: NovelImageTarget;
    character_id: string;
    look: string;
    visual_brief: string;
    render_prompt: string;
    preserve_features: string[];
    proposed_details: string[];
    open_questions: string[];
    consistency_checks: string[];
  };
}
const assistant = ref<{ project: Project; actor: Actor; branch: Branch }>();
const assistantBlocked = ref(false),
  suggestion = ref<VisualSuggestion>(),
  editedPrompt = ref("");
const assistantPayload = ref<Record<string, unknown>>({});
async function openAssistant() {
  if (busy.value || props.disabled) return;
  busy.value = true;
  error.value = "";
  const ticket = epoch;
  try {
    const workspace = await api<{
      project: Project;
      actor: Actor;
      branch: Branch;
    }>(`${root.value}/assistant`, "POST", {});
    if (!alive || ticket !== epoch) return;
    assistantPayload.value = {
      expected_ip_head: props.branchHead,
      character_id: props.characterId,
      novel: props.novel,
      look: look.value,
      references: [...references.value],
      previous_brief: instructions.value,
    };
    assistant.value = workspace;
    suggestion.value = undefined;
    editedPrompt.value = "";
  } catch (e) {
    if (alive && ticket === epoch) error.value = message(e);
  } finally {
    if (alive && ticket === epoch) busy.value = false;
  }
}
function showSuggestion(value: unknown) {
  suggestion.value = value as VisualSuggestion;
  editedPrompt.value = suggestion.value.proposal.render_prompt;
}
async function useSuggestion() {
  if (!suggestion.value || busy.value || props.disabled) return;
  const ticket = epoch;
  busy.value = true;
  error.value = "";
  try {
    const latest = await api<VisualSuggestion>(
      `${root.value}/assist/${suggestion.value.generation_id}`,
    );
    if (!alive || ticket !== epoch) return;
    if (
      latest.character_hash !== suggestion.value.character_hash ||
      (props.novel
        ? targetIdentity(latest.proposal.novel) !== targetIdentity(props.novel)
        : latest.proposal.character_id !== props.characterId) ||
      latest.proposal.look !== look.value
    )
      throw new Error("角色或造型已变化，请重新完善。");
    if (!editedPrompt.value.trim() || [...editedPrompt.value].length > 12000)
      throw new Error("请将本轮绘图要求编辑到12000字以内；内容没有被截断。");
    instructions.value = editedPrompt.value;
    references.value = latest.references.map((ref) => ref.id);
    visualGeneration.value = latest.generation_id;
    assistant.value = undefined;
    suggestion.value = undefined;
    assistantBlocked.value = false;
  } catch (e) {
    if (alive && ticket === epoch) error.value = message(e);
  } finally {
    if (alive && ticket === epoch) busy.value = false;
  }
}
const emit = defineEmits<{
  saved: [];
  blocking: [value: boolean];
  attention: [count: number];
}>();
function targetIdentity(value?: NovelImageTarget) {
  return JSON.stringify(
    value && [
      value.book_id,
      value.kind,
      value.name,
      value.source_ids,
      value.character_ids,
    ],
  );
}
interface Option {
  image_capabilities?: {
    image_input: boolean;
    image_generate: boolean;
    image_edit: boolean;
    region_edit: boolean;
  };
  model: string;
  name: string;
  price: { id: string; currency: string };
}
interface Task {
  id: string;
  state: string;
  paused: boolean;
  cancelled: boolean;
  frozen: {
    config: { model: string };
    estimate_micros: number;
    price: { currency: string };
    image: {
      look: string;
      character_id: string;
      references: { id: string }[];
      region?: ImageRegion;
    };
  };
  output?: { image?: { import_id: string } };
  call?: { state?: string; charged_micros: number; reserved_micros: number };
}
const options = ref<Option[]>([]),
  tasks = ref<Task[]>([]),
  selected = ref<Task>(),
  runtime = ref(false),
  busy = ref(false),
  error = ref("");
const attentionCount = computed(() => {
  const known = new Map(tasks.value.map((task) => [task.id, task]));
  if (selected.value) known.set(selected.value.id, selected.value);
  return [...known.values()].filter(
    (task) =>
      (task.state === "UNKNOWN" || task.call?.state === "UNKNOWN") &&
      task.call?.state !== "RECONCILED",
  ).length;
});
watch(attentionCount, (count) => emit("attention", count));
const look = ref("日常造型"),
  instructions = ref(""),
  references = ref<string[]>([]),
  size = ref("1536x1024"),
  quality = ref("medium"),
  reservation = ref(""),
  price = ref("");
const visualGeneration = ref("");
const regionEnabled = ref(false),
  region = ref<ImageRegion>();
const regionSource = computed(() =>
  props.images.find((i) => i.id === references.value[0]),
);
function resetRegion() {
  const source = regionSource.value;
  region.value = source
    ? {
        source_id: source.id,
        ...visualDimensions(source),
        areas: [],
        preserve: props.novel
          ? "保留主视觉、构图与未框选的场景和人物特征。"
          : "保留脸部、发型、体型及未框选部位的服饰道具。",
      }
    : undefined;
}
watch(
  () => regionSource.value?.id,
  () => {
    if (regionEnabled.value) resetRegion();
  },
);
watch(regionEnabled, (enabled) => {
  if (enabled) resetRegion();
  else region.value = undefined;
});
const root = computed(() =>
  props.novel
    ? `/novels/${props.novel.book_id}/images`
    : `/projects/${props.projectId}/branches/${props.branchId}/character-images`,
);
const taskRoot = computed(() => `/projects/${props.projectId}/generations`);
const option = computed(() =>
  options.value.find((o) => o.price.id === price.value),
);
const pending = computed(
  () =>
    busy.value ||
    !!assistant.value ||
    !!instructions.value ||
    references.value.length > 0,
);
watch(pending, (v) => emit("blocking", v));
let epoch = 0,
  alive = true,
  timer: ReturnType<typeof setTimeout> | undefined;
let attempt: { operation: string; body: string } | undefined;
function money(n: number, c: string) {
  return `${(n / 1000000).toFixed(6)} ${c}`;
}
const stateNames: Record<string, string> = {
  PREPARED: "已保存，等待开始",
  QUEUED: "排队中",
  RUNNING: "正在生成",
  SUCCEEDED: "图片已保存",
  CANCELLED: "已取消",
  UNKNOWN: "结果未知，费用占用保留",
  INVALID_OUTPUT: "未得到可用图片",
  REJECTED: "服务商拒绝请求",
  BLOCKED: "等待检查配置",
  RESTORED_DISABLED: "恢复副本，执行已停用",
};
function schedule() {
  if (timer) clearTimeout(timer);
  if (alive && tasks.value.some((t) => ["QUEUED", "RUNNING"].includes(t.state)))
    timer = setTimeout(() => void refresh(), 2500);
}
async function refresh() {
  const ticket = epoch,
    base = taskRoot.value;
  try {
    const [opts, list] = await Promise.all([
      api<{ items: Option[] }>(`${root.value}/generation-options`),
      api<{ items: Task[]; runtime_ready: boolean }>(
        `${base}?branch=${props.branchId}&${props.novel ? `image_novel=${props.novel.book_id}` : `character=${encodeURIComponent(props.characterId)}`}`,
      ),
    ]);
    if (!alive || ticket !== epoch) return;
    options.value = opts.items;
    tasks.value = list.items;
    runtime.value = list.runtime_ready;
    if (!price.value) price.value = opts.items[0]?.price.id ?? "";
    const id = selected.value?.id ?? tasks.value[0]?.id;
    if (id) {
      const value = await api<Task>(`${base}/${id}`);
      if (!alive || ticket !== epoch) return;
      const newlyDone =
        value.state === "SUCCEEDED" && selected.value?.state !== "SUCCEEDED";
      selected.value = value;
      if (newlyDone) emit("saved");
    }
    emit("attention", attentionCount.value);
    schedule();
  } catch (e) {
    if (alive && ticket === epoch) error.value = message(e);
  }
}
watch(
  () => [
    props.projectId,
    props.branchId,
    props.characterId,
    JSON.stringify(props.novel),
  ],
  () => {
    epoch++;
    if (timer) clearTimeout(timer);
    tasks.value = [];
    selected.value = undefined;
    instructions.value = "";
    look.value = props.novel?.name ?? "日常造型";
    size.value = props.novel?.kind === "cover" ? "1024x1536" : "1536x1024";
    assistant.value = undefined;
    suggestion.value = undefined;
    visualGeneration.value = "";
    references.value = [];
    regionEnabled.value = false;
    region.value = undefined;
    price.value = "";
    error.value = "";
    attempt = undefined;
    void refresh();
  },
  { immediate: true },
);
onUnmounted(() => {
  alive = false;
  epoch++;
  if (timer) clearTimeout(timer);
  emit("blocking", false);
});
async function prepare() {
  if (busy.value || props.disabled) return;
  const ticket = epoch;
  busy.value = true;
  error.value = "";
  try {
    if (
      !/^\d{1,6}(\.\d{1,6})?$/.test(reservation.value) ||
      Number(reservation.value) <= 0
    )
      throw new Error("填写本次费用预留金额，最多六位小数。");
    const [whole, fraction = ""] = reservation.value.split(".");
    const capability = option.value?.image_capabilities;
    if (
      !capability ||
      (references.value.length
        ? !capability.image_input || !capability.image_edit
        : !capability.image_generate) ||
      (regionEnabled.value && !capability.region_edit)
    )
      throw new Error(
        "当前模型尚未声明支持本次操作，请核对生图、参考图编辑和区域编辑能力后更新配置。",
      );
    if (
      regionEnabled.value &&
      (!region.value ||
        !region.value.areas.length ||
        !region.value.preserve.trim())
    )
      throw new Error("请先选择原图、框选修改区域，并填写必须保留的特征。");
    const body = {
      expected_ip_head: props.branchHead,
      character_id: props.characterId,
      novel: props.novel,
      look: look.value,
      instructions: instructions.value,
      visual_generation_id: visualGeneration.value || undefined,
      references: references.value,
      region: regionEnabled.value ? region.value : undefined,
      size: size.value,
      quality: quality.value,
      reservation_micros: Number(
        BigInt(whole ?? "0") * 1000000n + BigInt(fraction.padEnd(6, "0")),
      ),
      price_id: price.value,
    };
    const encoded = JSON.stringify(body);
    if (attempt?.body !== encoded)
      attempt = { operation: newOperationID(), body: encoded };
    const task = await api<Task>(`${root.value}/generate`, "POST", {
      ...body,
      operation_id: attempt.operation,
    });
    if (!alive || ticket !== epoch) return;
    selected.value = task;
    attempt = undefined;
    instructions.value = "";
    references.value = [];
    regionEnabled.value = false;
    region.value = undefined;
    await refresh();
  } catch (e) {
    if (alive && ticket === epoch) error.value = message(e);
  } finally {
    if (alive && ticket === epoch) busy.value = false;
  }
}
async function control(action: string) {
  if (!selected.value || busy.value || props.disabled) return;
  const ticket = epoch,
    id = selected.value.id;
  busy.value = true;
  error.value = "";
  try {
    const task = await api<Task>(
      `${taskRoot.value}/${id}/${action === "start" ? "start" : "control"}`,
      "POST",
      action === "start" ? {} : { action },
    );
    if (!alive || ticket !== epoch) return;
    selected.value = task;
    await refresh();
  } catch (e) {
    if (alive && ticket === epoch) error.value = message(e);
  } finally {
    if (alive && ticket === epoch) busy.value = false;
  }
}
async function choose(id: string) {
  const ticket = epoch;
  try {
    const task = await api<Task>(`${taskRoot.value}/${id}`);
    if (alive && ticket === epoch) selected.value = task;
  } catch (e) {
    if (alive && ticket === epoch) error.value = message(e);
  }
}
function revise() {
  regionEnabled.value = false;
  region.value = undefined;
  visualGeneration.value = "";
  if (!selected.value?.output?.image) return;
  references.value = [selected.value.output.image.import_id];
  if (!props.novel) look.value = selected.value.frozen.image.look;
  instructions.value = props.novel
    ? "保留主题、构图和未指定修改的场景与人物特征。本轮只修改："
    : "保留脸部、发型、体型和未指定修改的服饰道具。本轮只修改：";
}
</script>
<template>
  <section class="image-generator">
    <h3>{{ props.novel ? "AI 封面与场景图" : "AI 角色三视图" }}</h3>
    <p>
      默认目标：<strong>GPT Image 2.1</strong>。该精确型号的公开 API
      信息尚待核实。请在模型设置中填入核实后的型号和
      Key，绑定“图片生成”，并录入图片价格；系统不会自动换型。
    </p>
    <p v-if="!options.length" class="muted">
      尚无可用图片配置。可继续上传参考图、编辑视觉要求；配置齐全后保存生成任务。
    </p>
    <form @submit.prevent="prepare">
      <fieldset :disabled="busy || disabled || !!assistant">
        <label
          >{{ props.novel ? "图片方案名称" : "生成造型名称"
          }}<input
            v-model="look"
            :readonly="!!props.novel"
            required
            maxlength="120"
        /></label>
        <label
          >视觉要求 / 本轮修改意见<textarea
            v-model="instructions"
            rows="4"
            required
            maxlength="12000"
            placeholder="例如：水墨武侠风，青衣，左耳银环，正面、右侧面与背面。改图时写明只修改哪些部位。"
          />
        </label>
        <label
          >参考图片（可多选，最多16张）<select v-model="references" multiple>
            <option v-for="item in images" :key="item.id" :value="item.id">
              {{ visualLabel(item) }} · {{ item.id.slice(0, 8) }}
            </option>
          </select></label
        >
        <small
          >可先在下方上传参考图；选择已有生成图可继续改图。参考图固定为本次选择的版本。</small
        >
        <label class="region-toggle"
          ><input
            v-model="regionEnabled"
            type="checkbox"
            :disabled="!regionSource"
          />只修改框选区域</label
        >
        <p v-if="regionEnabled" class="muted">
          框选作用于第一张参考图；切换第一张图会清空选区。保存任务后可从历史恢复原图和选区。
        </p>
        <ImageRegionEditor
          v-if="regionEnabled && region"
          v-model="region"
          :src="`/api${root}/${region.source_id}/file`"
          :readonly="busy || disabled || !!assistant"
        />
        <label
          >实际调用模型<select v-model="price" required>
            <option value="" disabled>请选择已核实的图片配置</option>
            <option v-for="o in options" :key="o.price.id" :value="o.price.id">
              {{ o.name }} · {{ o.model }}
            </option>
          </select></label
        >
        <p v-if="option?.image_capabilities" class="muted">
          已声明能力：文生图{{
            option.image_capabilities.image_generate ? "支持" : "未确认"
          }}；参考图编辑{{
            option.image_capabilities.image_edit ? "支持" : "未确认"
          }}；局部区域编辑{{
            option.image_capabilities.region_edit ? "支持" : "未确认"
          }}。实际效果仍需验收。
        </p>
        <div class="settings">
          <label
            >画布<select v-model="size">
              <option value="1536x1024">横版 1536 × 1024</option>
              <option value="1024x1024">方形 1024 × 1024</option>
              <option value="1024x1536">竖版 1024 × 1536</option>
            </select></label
          ><label
            >质量<select v-model="quality">
              <option value="low">低</option>
              <option value="medium">中</option>
              <option value="high">高</option>
            </select></label
          ><label
            >本次费用预留 {{ option?.price.currency ?? ""
            }}<input
              v-model="reservation"
              inputmode="decimal"
              required
              placeholder="按已核实价格填写"
          /></label>
        </div>
        <small
          >预留金额用于本地预算控制，不是服务商收费硬上限；最终按回执结算。保存任务不会调用模型。</small
        >
        <button type="button" @click="openAssistant">让助手完善视觉要求</button>
        <button :disabled="!option || references.length > 16">
          {{ props.novel ? "保存小说图片生成任务" : "保存三视图生成任务" }}
        </button>
      </fieldset>
    </form>
    <section v-if="assistant" aria-label="视觉方案协作">
      <p>
        已自动准备可编辑的视觉方案 Agent 和
        Skill。此助手分析文字人设和参考图说明；参考图像素由后续图片模型处理，不把文字推断当作看图结论。
      </p>
      <GenerationPanel
        :project="assistant.project"
        :actor="assistant.actor"
        :branch="assistant.branch"
        :assistance="{
          root,
          kind: props.novel ? 'novel_visual' : 'character_visual',
          target: props.novel?.book_id,
          role: 'asset_designer',
          title: props.novel ? '小说视觉方案助手' : '角色视觉方案助手',
          payload: assistantPayload,
          notes:
            instructions ||
            (props.novel
              ? '根据小说主题、选中资料和角色整理封面或场景视觉要求；保留主线、人物特征和高光，推断单列为建议。'
              : '根据已保存的人设整理完整三视图视觉要求；未确定细节单列为建议，保留所有锁定特征。'),
        }"
        @blocking="assistantBlocked = $event"
        @assistance-suggestion="showSuggestion"
      />
      <div v-if="suggestion" class="visual-suggestion">
        <h4>视觉方案候选</h4>
        <p>
          采用时会一并使用这份方案固定的
          {{ suggestion.references.length }}
          张参考图。不会生成图片或修改正式人设。
        </p>
        <p>{{ suggestion.proposal.visual_brief }}</p>
        <h5>需要保留</h5>
        <ul>
          <li
            v-for="(text, i) in suggestion.proposal.preserve_features"
            :key="i"
          >
            {{ text }}
          </li>
        </ul>
        <h5>助手补充的建议</h5>
        <ul>
          <li
            v-for="(text, i) in suggestion.proposal.proposed_details"
            :key="i"
          >
            {{ text }}
          </li>
        </ul>
        <h5>待你确定</h5>
        <ul>
          <li v-for="(text, i) in suggestion.proposal.open_questions" :key="i">
            {{ text }}
          </li>
        </ul>
        <label
          >可编辑的绘图要求<textarea v-model="editedPrompt" rows="8" />
        </label>
        <details>
          <summary>出图后需要核对的项目</summary>
          <ul>
            <li
              v-for="(text, i) in suggestion.proposal.consistency_checks"
              :key="i"
            >
              {{ text }}
            </li>
          </ul>
        </details>
        <button
          type="button"
          :disabled="busy || disabled"
          @click="useSuggestion"
        >
          采用为本轮绘图要求
        </button>
      </div>
      <button
        type="button"
        :disabled="assistantBlocked || busy"
        @click="
          assistant = undefined;
          suggestion = undefined;
        "
      >
        返回图片生成
      </button>
    </section>
    <button type="button" :disabled="busy" @click="refresh">
      刷新任务与配置
    </button>
    <label v-if="tasks.length"
      >{{ props.novel ? "本书最近100次图片生成" : "本角色最近100次生成"
      }}<select
        :value="selected?.id"
        @change="choose(($event.target as HTMLSelectElement).value)"
      >
        <option v-for="task in tasks" :key="task.id" :value="task.id">
          {{ task.frozen.image.look }} ·
          {{ stateNames[task.state] ?? task.state }} · {{ task.id.slice(0, 8) }}
        </option>
      </select></label
    >
    <div v-if="selected" class="task">
      <p role="status">
        {{ stateNames[selected.state] ?? selected.state
        }}{{ selected.paused ? "（已暂停）" : "" }} · 实际模型
        {{ selected.frozen.config.model }}
      </p>
      <p>
        预留
        {{
          money(
            selected.frozen.estimate_micros,
            selected.frozen.price.currency,
          )
        }}<span v-if="selected.call">
          · 已记账
          {{
            money(selected.call.charged_micros, selected.frozen.price.currency)
          }}</span
        >
      </p>
      <p
        v-if="
          selected.state === 'UNKNOWN' && selected.call?.state !== 'RECONCILED'
        "
      >
        请求结果或用量尚不明确，保留费用占用，不自动重发。请先核对服务商回执。
      </p>
      <p v-else-if="selected.state === 'UNKNOWN'">
        生成结果仍未知；费用已按账单核对。
      </p>
      <p v-if="!runtime && selected.state === 'PREPARED'">
        任务已保存；任务执行服务就绪后可以开始。
      </p>
      <button
        v-if="selected.state === 'PREPARED'"
        type="button"
        :disabled="busy || disabled || !runtime"
        @click="control('start')"
      >
        按已授权预算开始生成
      </button>
      <button
        v-if="['PREPARED', 'QUEUED', 'RUNNING'].includes(selected.state)"
        type="button"
        :disabled="busy || disabled"
        @click="control(selected.paused ? 'resume' : 'pause')"
      >
        {{ selected.paused ? "恢复" : "暂停" }}任务
      </button>
      <button
        v-if="
          ['PREPARED', 'QUEUED', 'RUNNING', 'UNKNOWN'].includes(selected.state)
        "
        type="button"
        :disabled="busy || disabled"
        @click="control('cancel')"
      >
        取消任务
      </button>
      <template
        v-if="
          selected.output?.image &&
          selected.state === 'SUCCEEDED' &&
          !selected.cancelled
        "
        ><div class="comparison">
          <figure v-if="selected.frozen.image.references[0]">
            <img
              :src="`/api${root}/${selected.frozen.image.references[0].id}/file`"
              alt="修改前原图"
            />
            <figcaption>
              <a
                :href="`/api${root}/${selected.frozen.image.references[0].id}/file`"
                target="_blank"
                rel="noopener"
                >放大查看修改前原图</a
              >
            </figcaption>
          </figure>
          <figure>
            <img
              :src="`/api${root}/${selected.output.image.import_id}/file`"
              :alt="
                props.novel ? 'AI生成的小说图片候选' : 'AI生成的角色三视图候选'
              "
            />
            <figcaption>
              <a
                :href="`/api${root}/${selected.output.image.import_id}/file`"
                target="_blank"
                rel="noopener"
                >放大查看生成候选</a
              >
            </figcaption>
          </figure>
        </div>
        <p>
          {{
            props.novel
              ? "检查主题、构图、人物特征与场景细节。图片已保存在参考库，确认采用前仅为候选。"
              : "检查三面脸型、身高、服饰配色、左右位置与道具。图片已保存在下方参考库，确认采用前不会改变正式造型。"
          }}
        </p>
        <button type="button" :disabled="busy || disabled" @click="revise">
          以此图继续修改
        </button></template
      >
      <ImageRegionEditor
        v-if="selected.frozen.image.region"
        :model-value="selected.frozen.image.region"
        :src="`/api${root}/${selected.frozen.image.region.source_id}/file`"
        readonly
      />
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>
</template>
<style scoped>
.region-toggle {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.region-toggle input {
  width: auto;
  margin: 0;
}
.comparison {
  display: flex;
  flex-wrap: wrap;
  gap: 1rem;
}
.comparison figure {
  flex: 1 1 260px;
  margin: 0;
}
.image-generator {
  border: 1px solid var(--border, #ddd);
  padding: 1rem;
  border-radius: 12px;
  margin: 1rem 0;
}
label {
  display: block;
  margin: 0.7rem 0;
}
.settings {
  display: flex;
  flex-wrap: wrap;
  gap: 1rem;
}
textarea,
select,
input {
  max-width: 100%;
}
.task img {
  display: block;
  width: min(100%, 720px);
  max-height: 480px;
  object-fit: contain;
}
button {
  margin: 0.5rem 0.5rem 0.5rem 0;
}
small {
  display: block;
  color: var(--muted, #666);
}
</style>
