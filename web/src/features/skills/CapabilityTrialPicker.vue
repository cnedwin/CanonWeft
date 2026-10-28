<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, APIError, message } from "../../api";
import type { CapabilityManifest, CapabilityVersion } from "./capabilityTypes";
export interface TrialSelection {
  id: string;
  inputText: string;
  valid: boolean;
}
const props = defineProps<{
  project: string;
  role: string;
  managedInput?: boolean;
}>();
const emit = defineEmits<{ change: [TrialSelection | null]; expired: [] }>();
const versions = ref<CapabilityVersion[]>([]);
const cursor = ref("");
const selected = ref("");
const values = ref<Record<string, string>>({});
const rawMode = ref(false);
const rawInput = ref("{}");
const manifest = ref<CapabilityManifest | null>(null);
const error = ref("");
const loading = ref(false);
const choices = computed(() =>
  versions.value.filter(
    (v) =>
      !v.retention &&
      v.definition.kind === "agent" &&
      v.definition.role === props.role,
  ),
);
const version = computed(() =>
  choices.value.find((v) => v.id === selected.value),
);
const fields = computed(() =>
  Object.entries(
    (version.value?.definition.input_schema.properties ?? {}) as Record<
      string,
      { type?: string; title?: string }
    >,
  ),
);
const required = computed(
  () => (version.value?.definition.input_schema.required ?? []) as string[],
);
let disposed = false;
let nonce = 0;
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
function update() {
  if (!selected.value) {
    emit("change", null);
    return;
  }
  const entries: string[] = [];
  let valid = !!manifest.value?.trial_ready && !loading.value;
  if (props.managedInput) {
    emit("change", { id: selected.value, inputText: "", valid });
    return;
  }
  for (const [key, schema] of fields.value) {
    const text = values.value[key] ?? "";
    if (text === "" && !required.value.includes(key)) continue;
    try {
      if (schema.type !== "string") JSON.parse(text);
      entries.push(
        `${JSON.stringify(key)}:${schema.type === "string" ? JSON.stringify(text) : text}`,
      );
    } catch {
      valid = false;
    }
  }
  const inputText = rawMode.value ? rawInput.value : `{${entries.join(",")}}`;
  if (rawMode.value) {
    valid = !!manifest.value?.trial_ready && !loading.value;
    try {
      const value = JSON.parse(inputText);
      if (value === null || Array.isArray(value) || typeof value !== "object")
        valid = false;
    } catch {
      valid = false;
    }
  }
  emit("change", { id: selected.value, inputText, valid });
}
async function select() {
  const seq = ++nonce;
  values.value = {};
  rawInput.value = "{}";
  const schema = version.value?.definition.input_schema;
  rawMode.value =
    fields.value.length === 0 ||
    !!(
      schema?.allOf ||
      schema?.anyOf ||
      schema?.oneOf ||
      schema?.patternProperties
    );
  manifest.value = null;
  error.value = "";
  if (!selected.value) {
    update();
    return;
  }
  loading.value = true;
  update();
  try {
    const result = await api<CapabilityManifest>(
      `/projects/${props.project}/capabilities/${selected.value}/manifest`,
    );
    if (!disposed && seq === nonce) manifest.value = result;
  } catch (e) {
    if (!disposed && seq === nonce) fail(e);
  } finally {
    if (!disposed && seq === nonce) {
      loading.value = false;
      update();
    }
  }
}
async function load(more = false) {
  try {
    const result = await api<{
      items: CapabilityVersion[];
      next_cursor: string;
    }>(
      `/projects/${props.project}/capabilities?after=${more ? cursor.value : ""}`,
    );
    if (disposed) return;
    versions.value = more ? [...versions.value, ...result.items] : result.items;
    cursor.value = result.next_cursor;
  } catch (e) {
    if (!disposed) fail(e);
  }
}
watch(
  () => props.role,
  () => {
    ++nonce;
    selected.value = "";
    manifest.value = null;
    values.value = {};
    loading.value = false;
    update();
  },
);
watch(values, update, { deep: true });
watch([rawMode, rawInput], update);
onMounted(() => void load());
onBeforeUnmount(() => {
  disposed = true;
  ++nonce;
});
</script>
<template>
  <fieldset class="trial-picker">
    <legend>专业角色方法（可选试评）</legend>
    <label
      >固定角色版本<select
        v-model="selected"
        aria-label="固定角色版本"
        @change="select"
      >
        <option value="">
          {{ managedInput ? "系统预置方法" : "普通协作请求" }}
        </option>
        <option v-for="v in choices" :key="v.id" :value="v.id">
          {{ v.definition.name }} · {{ v.id.slice(0, 8) }}
        </option>
      </select></label
    >
    <p v-if="!choices.length">
      当前用途没有已保存的角色版本，可在方法与能力页面准备或编辑。
    </p>
    <button v-if="cursor" type="button" class="quiet" @click="load(true)">
      加载更多角色版本
    </button>
    <template v-if="version">
      <p>
        本次按固定方法和契约试评，不授予生产资格，不执行外部脚本。请求估算和付费开关照常生效。
      </p>
      <p v-if="managedInput">
        当前草稿、作者要求和资料由工作台填入；无需填写技术格式。可在方法与能力页面编辑方法后选择新版本试用。
      </p>
      <template v-else>
        <label
          ><input v-model="rawMode" type="checkbox" />使用完整 JSON
          输入（复杂契约）</label
        >
        <label v-if="rawMode"
          >完整试评输入<textarea
            v-model="rawInput"
            rows="8"
            required
            maxlength="262144"
          />
        </label>
        <p v-if="rawMode">
          保留完整对象和数字精度。保存请求时服务端按固定契约校验，未通过不会调用模型。
        </p>
        <template v-else
          ><label v-for="[key, schema] in fields" :key="key"
            >试评 · {{ schema.title ?? key
            }}<textarea
              v-model="values[key]"
              :required="required.includes(key)"
              rows="3"
              :placeholder="
                schema.type === 'string'
                  ? '输入文字或已整理的材料'
                  : '按契约填写 JSON 值'
              "
            />
          </label>
        </template>
      </template>
      <p v-if="loading">正在检查完整依赖与撤销状态…</p>
      <ul v-if="manifest && !manifest.trial_ready">
        <li v-for="(f, index) in manifest.findings" :key="index">
          {{ f.message }}
        </li>
      </ul>
    </template>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </fieldset>
</template>
