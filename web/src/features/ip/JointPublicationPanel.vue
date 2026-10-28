<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";

type Release = {
  id: string;
  hash: string;
  status: string;
  retention?: unknown;
  manifest: {
    branch_id: string;
    ip_candidate_id?: string;
    expected_ip_head: string;
    artifacts: { resource_id: string; hash: string; expected_head: string }[];
  };
  contents?: {
    resource_id: string;
    text: string;
    policy?: unknown;
    dependencies?: unknown;
  }[];
};
type Member = {
  release: Release;
  before: {
    id: string;
    name: string;
    text: string;
    policy?: unknown;
    dependencies?: unknown;
  }[];
  ipBefore?: unknown;
  ipAfter?: unknown;
};
const props = defineProps<{
  project: Project;
  actor: Actor;
  blocked: boolean;
}>();
const emit = defineEmits<{ blocking: [boolean]; expired: []; published: [] }>();
const root = `/projects/${props.project.id}`;
const branches = ref<{ id: string; name: string }[]>([]),
  branch = ref(""),
  choices = ref<Release[]>([]),
  choice = ref("");
const members = ref<Member[]>([]),
  confirmed = ref(false),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const pending = ref<{
  operation_id: string;
  releases: { release_id: string; manifest_hash: string }[];
} | null>(null);
const operation = ref(newOperationID());
const canPublish = computed(
  () =>
    ["owner", "publisher"].includes(props.actor.role) &&
    ["owner", "publisher"].includes(props.project.role),
);
let disposed = false;
watch(
  [busy, members, pending],
  () =>
    emit("blocking", busy.value || members.value.length > 0 || !!pending.value),
  { deep: true },
);
watch(branch, () => {
  choices.value = [];
  choice.value = "";
});
function branchName(id: string) {
  return branches.value.find((b) => b.id === id)?.name || id;
}
function reset() {
  confirmed.value = false;
  pending.value = null;
  operation.value = newOperationID();
}
async function perform(work: () => Promise<void>) {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    if (!disposed) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    busy.value = false;
  }
}
async function open(event: Event) {
  if (!(event.target as HTMLDetailsElement).open || branches.value.length)
    return;
  await perform(async () => {
    const data = await api<{ items: { id: string; name: string }[] }>(
      `${root}/branches`,
    );
    if (disposed) return;
    branches.value = data.items;
    branch.value = data.items[0]?.id || "";
  });
}
async function load() {
  await perform(async () => {
    const data = await api<{ items: Release[] }>(
      `${root}/releases?branch=${encodeURIComponent(branch.value)}`,
    );
    if (disposed) return;
    choices.value = data.items.filter(
      (r) => r.status === "READY" && !r.retention,
    );
    choice.value = choices.value[0]?.id || "";
  });
}
async function add() {
  await perform(async () => {
    if (!canPublish.value || !choice.value || members.value.length >= 16)
      return;
    const release = await api<Release>(`${root}/releases/${choice.value}`);
    if (
      release.status !== "READY" ||
      members.value.some(
        (m) => m.release.manifest.branch_id === release.manifest.branch_id,
      )
    )
      throw new Error(
        "每个分支请选择一份已采用清单；同分支修改请先合并为一份候选。",
      );
    const before = await Promise.all(
      release.manifest.artifacts.map((r) =>
        api<Member["before"][number]>(`${root}/resources/${r.resource_id}`),
      ),
    );
    let ipBefore: unknown, ipAfter: unknown;
    if (release.manifest.ip_candidate_id) {
      const current = await api<{ snapshot: unknown }>(
        `${root}/branches/${release.manifest.branch_id}`,
      );
      const candidate = await api<{ snapshot: unknown }>(
        `${root}/branches/${release.manifest.branch_id}/candidates/${release.manifest.ip_candidate_id}`,
      );
      ipBefore = current.snapshot;
      ipAfter = candidate.snapshot;
    }
    if (disposed) return;
    members.value.push({ release, before, ipBefore, ipAfter });
    reset();
  });
}
function remove(id: string) {
  members.value = members.value.filter((m) => m.release.id !== id);
  reset();
}
async function publish() {
  await perform(async () => {
    if (!canPublish.value || !confirmed.value || members.value.length < 2)
      return;
    if (!pending.value)
      pending.value = {
        operation_id: operation.value,
        releases: members.value
          .map((m) => ({
            release_id: m.release.id,
            manifest_hash: m.release.hash,
          }))
          .sort((a, b) => a.release_id.localeCompare(b.release_id)),
      };
    const result = await api<{ id: string; releases: Release[] }>(
      `${root}/releases/joint-publication`,
      "POST",
      pending.value,
    );
    if (disposed) return;
    notice.value = `${result.releases.length} 份清单已一起发布。联合记录：${result.id}`;
    members.value = [];
    reset();
    choices.value = [];
    choice.value = "";
    emit("published");
  });
}
function guardUnload(event: BeforeUnloadEvent) {
  if (members.value.length || busy.value) {
    event.preventDefault();
    event.returnValue = "";
  }
}
window.addEventListener("beforeunload", guardUnload);
onUnmounted(() => {
  disposed = true;
  window.removeEventListener("beforeunload", guardUnload);
  emit("blocking", false);
});
</script>

<template>
  <details @toggle="open">
    <summary>跨分支一起发布</summary>
    <section role="group" aria-label="跨分支发布选择">
      <p>
        选择各分支已经采用的清单，逐份核对后一起发布。任一版本、复核或索引失效时，整组保持原状。
      </p>
      <p>
        每个分支一份清单，本组2—16份。同分支修改先合并；相互引用新版本的清单须先明确绑定，系统不会改写已确认内容。
      </p>
      <p v-if="error" role="alert">{{ error }}</p>
      <p v-if="notice" role="status">{{ notice }}</p>
      <fieldset :disabled="busy || blocked || !canPublish">
        <label
          >选择发布分支<select v-model="branch" aria-label="选择发布分支">
            <option v-for="b in branches" :key="b.id" :value="b.id">
              {{ b.name }}
            </option>
          </select></label
        >
        <button type="button" :disabled="!branch" @click="load">
          读取该分支已采用候选
        </button>
        <label
          >选择已采用清单<select v-model="choice" aria-label="选择已采用清单">
            <option value="">请选择</option>
            <option v-for="r in choices" :key="r.id" :value="r.id">
              {{ r.manifest.artifacts.length }} 份资料 · {{ r.id }}
            </option>
          </select></label
        >
        <p>
          从该分支最近100份候选中列出已采用项。已采用不保证发布时仍有效，服务端会再次核对。
        </p>
        <button
          type="button"
          :disabled="!choice || members.length >= 16 || !!pending"
          @click="add"
        >
          加入并查看完整对照
        </button>
        <article v-for="member in members" :key="member.release.id">
          <h4>
            {{ branchName(member.release.manifest.branch_id) }} ·
            {{ member.release.manifest.artifacts.length }} 份资料
          </h4>
          <button
            type="button"
            :disabled="!!pending"
            @click="remove(member.release.id)"
          >
            移出本组
          </button>
          <article
            v-for="content in member.release.contents"
            :key="content.resource_id"
          >
            <h5>
              {{
                member.before.find((r) => r.id === content.resource_id)?.name ||
                content.resource_id
              }}
            </h5>
            <div class="joint-compare">
              <div>
                <strong>当前正式内容</strong>
                <pre>{{
                  member.before.find((r) => r.id === content.resource_id)
                    ?.text || "尚无正式内容"
                }}</pre>
              </div>
              <div>
                <strong>已采用候选内容</strong>
                <pre>{{ content.text }}</pre>
              </div>
            </div>
            <details>
              <summary>查看用途与依赖变化</summary>
              <pre>{{
                JSON.stringify(
                  {
                    原用途: member.before.find(
                      (r) => r.id === content.resource_id,
                    )?.policy,
                    新用途: content.policy,
                    原依赖: member.before.find(
                      (r) => r.id === content.resource_id,
                    )?.dependencies,
                    新依赖: content.dependencies,
                  },
                  null,
                  2,
                )
              }}</pre>
            </details>
          </article>
          <details v-if="member.ipAfter">
            <summary>查看随本组发布的设定变化</summary>
            <div class="joint-compare">
              <pre>{{ JSON.stringify(member.ipBefore, null, 2) }}</pre>
              <pre>{{ JSON.stringify(member.ipAfter, null, 2) }}</pre>
            </div>
          </details>
        </article>
        <label class="joint-confirm"
          ><input
            v-model="confirmed"
            type="checkbox"
            :disabled="members.length < 2 || !!pending"
          />我已核对所有分支的变更内容</label
        >
        <button
          type="button"
          :disabled="members.length < 2 || !confirmed"
          @click="publish"
        >
          确认这组内容并一起发布
        </button>
        <button
          type="button"
          :disabled="!members.length"
          @click="
            members = [];
            reset();
          "
        >
          放弃本次选择
        </button>
        <p v-if="pending">
          请求已固定。回复中断时可直接重试；不会更换清单或重复发布。
        </p>
      </fieldset>
    </section>
  </details>
</template>

<style scoped>
.joint-compare {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.joint-confirm {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.joint-confirm input {
  width: auto;
  margin: 0;
}
@media (max-width: 700px) {
  .joint-compare {
    grid-template-columns: 1fr;
  }
}
</style>
