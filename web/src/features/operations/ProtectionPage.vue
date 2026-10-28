<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { api, APIError, message, type Actor, type Project } from "../../api";
import RetentionPlans from "./RetentionPlans.vue";
import RecycleBin from "./RecycleBin.vue";

interface ProtectionStatus {
  checkpoint_state: string;
  attempt_state: string;
  checked_at: string;
  running: boolean;
  retention_days: number;
  latest: { id: string; snapshot_at: string; kind: string; bytes: number };
  log_coverage?: {
    state: string;
    checked_at: string;
    coverage_from: string;
    latest: { observed_at: string; last_commit_at?: string };
    running: boolean;
    history_gaps: number;
    cleanup_state: string;
  };
}

const emit = defineEmits<{
  close: [];
  expired: [];
  recovered: [project: Project];
}>();
defineProps<{ actor: Actor; projects: Project[] }>();
const status = ref<ProtectionStatus | null>(null);
const error = ref("");
const loading = ref(false);
const downloading = ref(false);
const diagnosticError = ref("");
const inventory = ref<{
  captured_at: string;
  project_count: number;
  project_records: number;
  shared_records: number;
  private_value_bytes: number;
  adopted_content_references: number;
  adopted_content_objects: number;
  author_content_references: number;
  author_content_objects: number;
} | null>(null);
const inventoryLoading = ref(false);
const inventoryError = ref("");
const clockNow = ref(Date.now());
let active = true;
let timer: ReturnType<typeof setInterval> | undefined;

const labels: Record<string, string> = {
  CURRENT: "已有最近的恢复点",
  STALE: "恢复点已超过 15 分钟，请检查保护状态",
  MISSING: "尚未建立恢复点",
  CORRUPT: "恢复点校验失败，需要处理",
  CLOCK_ERROR: "系统时间异常，需要检查",
  UNAVAILABLE: "暂时无法确认保护状态",
};
const attempts: Record<string, string> = {
  STARTING: "正在启动自动保护",
  CURRENT: "最近一次检查正常",
  STALE: "恢复点已过期",
  MISSING: "正在准备首次备份",
  CAPTURE_FAILED: "最近一次备份失败，原有恢复点未被覆盖",
  HEALTH_WRITE_FAILED: "运行记录保存失败，请检查磁盘空间和目录权限",
  ERROR: "自动保护需要检查",
  CORRUPT: "备份文件损坏，需要检查存储介质",
};
const checkpointState = computed(() => {
  if (error.value) return "UNAVAILABLE";
  if (!status.value) return "MISSING";
  const current = status.value.checkpoint_state;
  if (current !== "CURRENT") return current;
  const snapshotAt = Date.parse(status.value.latest.snapshot_at);
  if (!status.value.latest.id || !Number.isFinite(snapshotAt))
    return "UNAVAILABLE";
  const age = clockNow.value - snapshotAt;
  if (age > 15 * 60 * 1000) return "STALE";
  if (age < -60 * 1000) return "CLOCK_ERROR";
  return "CURRENT";
});
const healthy = computed(() => checkpointState.value === "CURRENT");
const logLabels: Record<string, string> = {
  CURRENT: "连续日志已保存",
  CURRENT_WITH_GAPS: "新的日志保护正常，历史中存在缺口",
  STALE: "日志恢复点已超过 15 分钟，请检查保护状态",
  NOT_CONFIGURED: "连续日志保护尚未配置",
  STARTING: "正在建立连续日志保护",
  CAPTURE_FAILED: "日志采集失败，正在准备新的保护起点",
  BASELINE_FAILED: "日志基线建立失败，请检查备份账号与存储",
  CLOCK_ERROR: "日志源时间异常，需要检查",
  ERROR: "连续日志保护需要检查",
  UNAVAILABLE: "暂时无法确认连续日志状态",
};
const logState = computed(() => {
  if (error.value) return "UNAVAILABLE";
  const logs = status.value?.log_coverage;
  if (!logs) return "NOT_CONFIGURED";
  if (logs.state !== "CURRENT" && logs.state !== "CURRENT_WITH_GAPS")
    return logs.state;
  const observed = Date.parse(logs.latest.observed_at);
  if (!Number.isFinite(observed) || logs.latest.observed_at.startsWith("0001-"))
    return "UNAVAILABLE";
  if (clockNow.value - observed > 15 * 60 * 1000) return "STALE";
  if (observed - clockNow.value > 60 * 1000) return "CLOCK_ERROR";
  return logs.state;
});

function formatTime(value: string | undefined) {
  if (!value || value.startsWith("0001-")) return "尚无记录";
  return new Date(value).toLocaleString("zh-CN", { hour12: false });
}

async function refresh() {
  if (loading.value) return;
  loading.value = true;
  try {
    const result = await api<ProtectionStatus>("/operations/protection");
    if (!active) return;
    status.value = result;
    clockNow.value = Date.now();
    error.value = "";
  } catch (e) {
    if (!active) return;
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    if (active) loading.value = false;
  }
}

async function downloadDiagnostics() {
  if (downloading.value) return;
  downloading.value = true;
  diagnosticError.value = "";
  try {
    const report = await api<Record<string, unknown>>(
      "/operations/diagnostics",
    );
    if (!active) return;
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(report, null, 2)], {
        type: "application/json;charset=utf-8",
      }),
    );
    const link = document.createElement("a");
    link.href = url;
    link.download = "studio-diagnostics.json";
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch (e) {
    if (!active) return;
    diagnosticError.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    if (active) downloading.value = false;
  }
}

async function checkInventory() {
  if (inventoryLoading.value) return;
  inventoryLoading.value = true;
  inventoryError.value = "";
  try {
    const report = await api<NonNullable<typeof inventory.value>>(
      "/operations/retention-inventory",
    );
    if (active) inventory.value = report;
  } catch (e) {
    if (!active) return;
    inventory.value = null;
    inventoryError.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    if (active) inventoryLoading.value = false;
  }
}

onMounted(() => {
  void refresh();
  timer = setInterval(() => {
    clockNow.value = Date.now();
    void refresh();
  }, 10_000);
});
onUnmounted(() => {
  active = false;
  if (timer) clearInterval(timer);
});
</script>

<template>
  <main class="workspace">
    <section class="intro">
      <div>
        <p class="eyebrow">工作室 / 备份保护</p>
        <h1>作品的备份保护</h1>
        <p class="muted">
          工作室运行时自动保存恢复点，日常写作不需要手动导出。
        </p>
      </div>
      <button class="quiet" @click="emit('close')">返回工作室</button>
    </section>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <section class="panel">
      <h2>恢复点</h2>
      <p v-if="status" :class="healthy ? 'notice' : 'error'" role="status">
        {{ labels[checkpointState] ?? "正在检查恢复点" }}
      </p>
      <p v-else>正在读取自动保护状态…</p>
      <dl v-if="status">
        <dt>可恢复到的时间</dt>
        <dd>{{ formatTime(status.latest.snapshot_at) }}</dd>
        <dt>最近检查</dt>
        <dd>{{ formatTime(status.checked_at) }}</dd>
        <dt>当前运行</dt>
        <dd>
          {{
            status.running
              ? "正在检查或保存恢复点"
              : (attempts[status.attempt_state] ?? "需要检查")
          }}
        </dd>
        <dt>保留期限</dt>
        <dd>{{ status.retention_days }} 天，到期自动清理；异常时会提示</dd>
      </dl>
      <button :disabled="loading" @click="refresh">刷新状态</button>
    </section>
    <section class="panel">
      <h2>连续日志保护</h2>
      <p :class="logState === 'CURRENT' ? 'notice' : 'error'" role="status">
        {{ logLabels[logState] ?? "连续日志保护需要检查" }}
      </p>
      <dl v-if="status?.log_coverage">
        <dt>本段保护起点</dt>
        <dd>{{ formatTime(status.log_coverage.coverage_from) }}</dd>
        <dt>日志已覆盖到</dt>
        <dd>{{ formatTime(status.log_coverage.latest.observed_at) }}</dd>
        <dt>最近检查</dt>
        <dd>{{ formatTime(status.log_coverage.checked_at) }}</dd>
        <dt>已记录的历史缺口</dt>
        <dd>{{ status.log_coverage.history_gaps }} 段</dd>
      </dl>
      <p v-if="status?.log_coverage?.history_gaps" class="error">
        历史缺口不会因建立新备份而消失。恢复时只能选择已验证的完整区间或快照。
      </p>
      <p
        v-if="status?.log_coverage?.cleanup_state === 'NEEDS_ATTENTION'"
        class="error"
      >
        部分过期文件清理失败，请检查文件占用、损坏和存储权限；其他可清理副本会继续处理。
      </p>
      <p class="muted">
        日志恢复以已记录的完整提交为边界。日志缺失或损坏时不会静默退回较旧版本。
      </p>
    </section>
    <section class="panel">
      <h2>保护范围</h2>
      <p>
        每天生成基线快照，中间每 10 分钟补充恢复点；备份失败不会覆盖已有副本。
      </p>
      <p>
        恢复时建立独立副本，并重新检查当前删除和撤销记录。密钥与登录凭据不进入作品备份。
      </p>
      <p class="muted">独立介质恢复尚未验收。</p>
      <p class="muted">
        本机快照不能替代独立备份介质。恢复点时间反映实际可恢复的数据时间，不是最近一次尝试时间。
      </p>
    </section>
    <section class="panel">
      <h2>资料归属检查</h2>
      <p class="muted">
        检查作品资料与工作室共享资料是否具有明确归属。此检查只读取统计信息，不删除或修改资料。
      </p>
      <p v-if="inventoryError" class="error" role="alert">
        {{ inventoryError }}
      </p>
      <dl v-if="inventory">
        <dt>检查时间</dt>
        <dd>{{ formatTime(inventory.captured_at) }}</dd>
        <dt>作品数量（含回收中的作品）</dt>
        <dd>{{ inventory.project_count }} 部</dd>
        <dt>作品专属记录</dt>
        <dd>{{ inventory.project_records }} 条</dd>
        <dt>工作室共享记录</dt>
        <dd>{{ inventory.shared_records }} 条</dd>
        <dt>已采用内容引用（含历史版本）</dt>
        <dd>{{ inventory.adopted_content_references }} 处</dd>
        <dt>关联内容文件</dt>
        <dd>
          {{ inventory.adopted_content_objects }} 个（尚需核验文件完整性）
        </dd>
        <dt>作者草稿与能力文件引用（含历史版本）</dt>
        <dd>{{ inventory.author_content_references }} 处</dd>
        <dt>草稿与能力关联文件</dt>
        <dd>{{ inventory.author_content_objects }} 个（尚需核验文件完整性）</dd>
        <dt>数据库中的私有资料</dt>
        <dd>
          {{ (inventory.private_value_bytes / 1024).toFixed(1) }}
          KB（不含附件文件）
        </dd>
      </dl>
      <p class="muted">
        归属检查通过不代表资料已到期，清理仍需核对引用、保留期限和恢复保护。
      </p>
      <button :disabled="inventoryLoading" @click="checkInventory">
        {{ inventoryLoading ? "正在检查资料归属…" : "检查资料归属" }}
      </button>
    </section>
    <RecycleBin
      :actor="actor"
      @expired="emit('expired')"
      @recovered="emit('recovered', $event)"
    />
    <RetentionPlans
      :actor="actor"
      :projects="projects"
      @expired="emit('expired')"
    />
    <section class="panel">
      <h2>诊断信息</h2>
      <p class="muted">
        导出运行版本、任务数量和保护状态，便于排查问题；文件不包含小说正文、人物资料、提示词或密钥。
      </p>
      <p v-if="diagnosticError" class="error" role="alert">
        {{ diagnosticError }}
      </p>
      <button :disabled="downloading" @click="downloadDiagnostics">
        {{ downloading ? "正在整理诊断信息…" : "导出诊断信息" }}
      </button>
    </section>
  </main>
</template>
