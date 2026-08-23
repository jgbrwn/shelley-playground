<!-- DeployModal.vue — "Deploy to new exe.dev VM" (forklift). Creates a fresh
     exe.dev VM via the exe.dev API, rsyncs the project directory to the same
     absolute path, and reconciles system state (apt/pip/npm packages,
     systemd units, users, crontabs) by diffing source vs destination.
     Live progress streams over SSE into an in-modal console. -->
<template>
  <Modal :is-open="isOpen" title="Deploy project to a new exe.dev VM" @close="onClose">
    <div class="deploy-form">
      <div class="deploy-field">
        <label for="deploy-vm-name">New VM name</label>
        <InputText
          id="deploy-vm-name"
          v-model="vmName"
          placeholder="my-app-prod"
          :disabled="running"
          fluid
          :dt="inputFieldDt"
        />
        <small class="deploy-hint">Lowercase letters, digits and hyphens. Accessible at https://name.exe.xyz</small>
      </div>

      <div class="deploy-field">
        <label for="deploy-image">Image</label>
        <InputText
          id="deploy-image"
          v-model="image"
          placeholder="(default: exeuntu)"
          :disabled="running"
          fluid
          :dt="inputFieldDt"
        />
        <small class="deploy-hint">Leave blank to use the standard exeuntu image.</small>
      </div>

      <div class="deploy-field">
        <label for="deploy-dir">Project directory</label>
        <InputText
          id="deploy-dir"
          v-model="projectDir"
          placeholder="/home/exedev/playground/my-project"
          :disabled="running"
          fluid
          :dt="inputFieldDt"
        />
        <small class="deploy-hint">Copied (rsync) to the same path on the new VM.</small>
      </div>

      <div class="deploy-field">
        <label for="deploy-key">exe.dev API key</label>
        <Password
          v-if="!useSavedKey"
          id="deploy-key"
          v-model="apiKey"
          :feedback="false"
          toggle-mask
          placeholder="exe1.… or exe0.…"
          :disabled="running"
          fluid
          :dt="inputFieldDt"
        />
        <div v-else class="deploy-saved-key">
          <code>{{ maskedKey || "(none saved)" }}</code>
          <Button label="Replace" text size="small" :disabled="running" @click="replaceKey" />
        </div>
        <small class="deploy-hint">
          Create one with
          <code>ssh exe.dev ssh-key generate-api-key --cmds=whoami,ls,new --exp=90d</code>.
          {{ savedHint }}
        </small>
      </div>

      <label class="deploy-dryrun">
        <input type="checkbox" v-model="dryRun" :disabled="running" />
        Dry run — validate the key and show the plan without creating anything
      </label>

      <div v-if="formError" class="deploy-error">{{ formError }}</div>

      <div class="deploy-actions">
        <Button
          :label="running ? 'Deploying…' : dryRun ? 'Run dry run' : 'Deploy'"
          icon="pi pi-upload"
          :loading="starting || running"
          :disabled="starting || running"
          @click="start"
        />
        <Button
          v-if="running"
          label="Cancel"
          severity="danger"
          text
          @click="cancel"
        />
        <Button v-if="!running && events.length" label="Clear console" severity="secondary" text @click="clearConsole" />
      </div>
    </div>

    <div v-if="events.length || running" class="deploy-console" ref="consoleRef">
      <div v-for="(e, i) in events" :key="i" :class="['deploy-line', `deploy-${e.level}`]">
        <span class="deploy-time">{{ shortTime(e.time) }}</span>
        <span class="deploy-step">[{{ e.step }}]</span>
        <span class="deploy-msg">{{ e.message }}</span>
      </div>
      <div v-if="finished === 'success'" class="deploy-line deploy-success deploy-final">✅ Deploy finished</div>
      <div v-else-if="finished === 'failed'" class="deploy-line deploy-error deploy-final">❌ Deploy failed</div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { nextTick, ref, watch } from "vue";
import Modal from "./Modal.vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Password from "primevue/password";
import { inputFieldDt } from "./configFieldDt";
import { deployApi, type DeployEvent } from "../../services/api";

const props = defineProps<{ isOpen: boolean; suggestedDir?: string }>();
const emit = defineEmits<{ (e: "close"): void }>();

const vmName = ref("");
const image = ref("");
const projectDir = ref(props.suggestedDir ?? "");
const apiKey = ref("");
const useSavedKey = ref(true);
const maskedKey = ref("");
const dryRun = ref(false);
const starting = ref(false);
const running = ref(false);
const formError = ref("");
const events = ref<DeployEvent[]>([]);
const finished = ref<"" | "success" | "failed">("");
const consoleRef = ref<HTMLElement | null>(null);
let es: EventSource | null = null;

const savedHint = ref("");

watch(
  () => props.isOpen,
  async (open) => {
    if (!open) return;
    try {
      const s = await deployApi.getSettings();
      maskedKey.value = s.api_key_masked;
      image.value = s.default_image;
      if (!projectDir.value && props.suggestedDir) projectDir.value = props.suggestedDir;
      savedHint.value = s.api_key_masked ? "The saved key will be used unless you replace it." : "";
    } catch {
      /* settings endpoint failure shouldn't block the modal */
    }
  },
);

function replaceKey() {
  useSavedKey.value = false;
  apiKey.value = "";
}

async function start() {
  formError.value = "";
  starting.value = true;
  events.value = [];
  finished.value = "";
  try {
    // If the user typed a new key, save it first so future deploys reuse it.
    if (!useSavedKey.value && apiKey.value.trim()) {
      await deployApi.putSettings(apiKey.value.trim());
      useSavedKey.value = true;
      const s = await deployApi.getSettings();
      maskedKey.value = s.api_key_masked;
    }
    await deployApi.start({
      vm_name: vmName.value.trim(),
      image: image.value.trim(),
      project_dir: projectDir.value.trim(),
      dry_run: dryRun.value,
      api_key: "", // already persisted above when replaced
    });
    starting.value = false;
    running.value = true;
    listen();
  } catch (err) {
    starting.value = false;
    formError.value = String(err instanceof Error ? err.message : err);
  }
}

function listen() {
  es?.close();
  es = new EventSource("/api/deploy/current");
  es.onmessage = (m) => {
    try {
      const data = JSON.parse(m.data);
      if (data.type === "idle") {
        running.value = false;
        es?.close();
        return;
      }
      if (data.type === "finished") {
        finished.value = data.status;
        running.value = false;
        es?.close();
        return;
      }
      events.value.push(data as DeployEvent);
      scrollConsole();
    } catch {
      /* ignore malformed frames */
    }
  };
  es.onerror = () => {
    // The stream ends naturally on completion; only surface errors mid-run.
    if (running.value) {
      setTimeout(() => {
        if (running.value) listen();
      }, 1000);
    }
  };
}

async function cancel() {
  try {
    await deployApi.cancel();
  } catch {
    /* run may have just finished */
  }
}

function clearConsole() {
  events.value = [];
  finished.value = "";
}

function scrollConsole() {
  nextTick(() => {
    if (consoleRef.value) consoleRef.value.scrollTop = consoleRef.value.scrollHeight;
  });
}

function shortTime(t: string) {
  return t.slice(11, 19);
}

function onClose() {
  emit("close");
}
</script>

<style scoped>
.deploy-form {
  display: flex;
  flex-direction: column;
  gap: 0.9rem;
}
.deploy-field {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
}
.deploy-field label {
  font-weight: 600;
  font-size: 0.85rem;
}
.deploy-hint {
  color: var(--p-text-muted-color, #888);
  font-size: 0.78rem;
}
.deploy-saved-key {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.deploy-dryrun {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.85rem;
}
.deploy-error {
  color: var(--p-red-500, #ef4444);
  font-size: 0.85rem;
  white-space: pre-wrap;
}
.deploy-actions {
  display: flex;
  gap: 0.5rem;
  align-items: center;
}
.deploy-console {
  margin-top: 1rem;
  max-height: 320px;
  overflow-y: auto;
  background: var(--p-surface-900, #1a1a1a);
  color: #d4d4d4;
  border-radius: 6px;
  padding: 0.6rem;
  font-family: ui-monospace, monospace;
  font-size: 0.78rem;
  line-height: 1.5;
}
.deploy-line {
  white-space: pre-wrap;
  word-break: break-word;
}
.deploy-time {
  opacity: 0.55;
  margin-right: 0.5rem;
}
.deploy-step {
  font-weight: 700;
  margin-right: 0.5rem;
}
.deploy-warn .deploy-msg {
  color: #fbbf24;
}
.deploy-error .deploy-msg,
.deploy-final.deploy-error {
  color: #f87171;
}
.deploy-success .deploy-msg,
.deploy-final.deploy-success {
  color: #4ade80;
}
.deploy-cmd .deploy-msg {
  color: #93c5fd;
}
</style>
