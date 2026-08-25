<!-- DeployModal.vue — "Deploy to new exe.dev VM" (forklift). Creates a fresh
     exe.dev VM via the exe.dev API, rsyncs the project directory to the same
     absolute path, and reconciles system state (apt/pip/npm packages,
     systemd units, users, crontabs) by diffing source vs destination.
     Live progress streams over SSE into an in-modal console. -->
<template>
  <Modal :is-open="isOpen" title="Deploy project to a new exe.dev VM" @close="onClose">
    <div class="deploy-form">
      <div class="deploy-field">
        <label for="deploy-vm-name">New VM name <span class="deploy-required">*</span></label>
        <InputText
          id="deploy-vm-name"
          v-model="vmName"
          placeholder="my-app-prod"
          :disabled="starting || running"
          :invalid="Boolean(vmNameError)"
          aria-required="true"
          fluid
          :dt="inputFieldDt"
        />
        <small v-if="vmNameError" class="deploy-field-error">{{ vmNameError }}</small>
        <small v-else class="deploy-hint">Lowercase letters, digits and hyphens. Accessible at https://name.exe.xyz</small>
      </div>

      <div class="deploy-field">
        <label for="deploy-image">Image <span class="deploy-optional">(optional)</span></label>
        <InputText
          id="deploy-image"
          v-model="image"
          placeholder="(standard exeuntu)"
          :disabled="starting || running"
          :invalid="Boolean(imageError)"
          fluid
          :dt="inputFieldDt"
        />
        <small v-if="imageError" class="deploy-field-error">{{ imageError }}</small>
        <small v-else class="deploy-hint">Leave blank to use standard exeuntu. Custom images must be Ubuntu/Debian-based.</small>
      </div>

      <div class="deploy-field">
        <label for="deploy-dir">Project directory <span class="deploy-required">*</span></label>
        <InputText
          id="deploy-dir"
          v-model="projectDir"
          placeholder="/home/exedev/playground/my-project"
          :disabled="starting || running"
          :invalid="Boolean(projectDirError)"
          aria-required="true"
          fluid
          :dt="inputFieldDt"
        />
        <small v-if="projectDirError" class="deploy-field-error">{{ projectDirError }}</small>
        <small v-else class="deploy-hint">Copied to an empty /home/exedev/&lt;project-name&gt; directory on the new VM.</small>
      </div>

      <div class="deploy-field-row">
        <div class="deploy-field">
          <label for="deploy-port">App port <span class="deploy-optional">(optional)</span></label>
          <InputText
            id="deploy-port"
            v-model="port"
            :placeholder="detectedPorts.length ? detectedPorts.join(', ') + ' detected' : '8000'"
            :disabled="starting || running"
            :invalid="Boolean(portError)"
            fluid
            :dt="inputFieldDt"
          />
          <small :class="portError ? 'deploy-field-error' : 'deploy-hint'">
            {{ portError || portHint }}
          </small>
        </div>
        <label class="deploy-public">
          <input type="checkbox" v-model="makePublic" :disabled="starting || running" />
          Make Public
        </label>
      </div>

      <div class="deploy-field">
        <label for="deploy-key">exe.dev API key <span class="deploy-required">*</span></label>
        <Password
          v-if="!useSavedKey"
          id="deploy-key"
          v-model="apiKey"
          :feedback="false"
          toggle-mask
          placeholder="exe1.… or exe0.…"
          :disabled="starting || running"
          :invalid="Boolean(apiKeyError)"
          aria-required="true"
          fluid
          :dt="inputFieldDt"
        />
        <div v-else class="deploy-saved-key">
          <code>{{ maskedKey }}</code>
          <Button label="Replace" text size="small" :disabled="starting || running" @click="replaceKey" />
        </div>
        <small v-if="apiKeyError" class="deploy-field-error">{{ apiKeyError }}</small>
        <small v-else class="deploy-hint">
          Create one with
          <code>ssh exe.dev ssh-key generate-api-key --cmds=whoami,ls,new,ssh-key\ list,ssh-key\ add,share\ port,share\ set-public,rm --exp=90d</code>.
          {{ savedHint }}
        </small>
      </div>

      <label class="deploy-dryrun">
        <input type="checkbox" v-model="dryRun" :disabled="starting || running" />
        Dry run — validates the key and generates the dependency report (markdown, copy-pastable) without creating anything
      </label>

      <div>
        <label class="deploy-dryrun" :class="{ 'deploy-disabled': !fullCloneSupported }"
          :title="fullCloneSupported ? '' : `Full state clone requires a debian/ubuntu amd64 source host (this host: ${sourceOS})`">
          <input type="checkbox" v-model="fullClone" :disabled="starting || running || !fullCloneSupported" />
          Full state clone — mirror ALL packages from source OS (apt/pip/npm wholesale)
        </label>
        <small v-if="!fullCloneSupported" class="deploy-hint">
          Unavailable on {{ sourceOS }} — requires a debian/ubuntu amd64 source host. Minimal (project-scoped) mode will be used.
        </small>
        <small v-else-if="fullClone" class="deploy-hint deploy-warn-hint">
          ⚠️ This installs every package from this source VM onto the destination — including unrelated projects' dependencies. Minimal mode is recommended.
        </small>
      </div>

      <label class="deploy-dryrun">
        <input type="checkbox" v-model="skipSystemd" :disabled="starting || running" />
        Skip systemd — don't copy/create units or enforce loopback binding (handle both yourself)
      </label>

      <div v-if="formError" class="deploy-error">{{ formError }}</div>

      <div class="deploy-actions">
        <Button
          :label="running ? 'Deploying…' : dryRun ? 'Run dry run' : 'Deploy'"
          icon="pi pi-upload"
          :loading="starting || running"
          :disabled="starting || running || !formValid"
          @click="start"
        />
        <Button
          v-if="running"
          label="Cancel"
          severity="danger"
          text
          @click="cancel"
        />
        <Button
          v-if="dryRun && !running && events.length"
          label="Copy dry run"
          severity="secondary"
          text
          icon="pi pi-copy"
          @click="copyDryRun"
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
      <div v-else-if="finished === 'failed'" class="deploy-line deploy-error deploy-final">
        ❌ Deploy failed
        <Button
          v-if="failedVMName"
          class="deploy-delete-btn"
          :label="deletingVM ? 'Deleting…' : `Delete VM ${failedVMName}`"
          severity="danger"
          size="small"
          :loading="deletingVM"
          :disabled="deletingVM"
          @click="deleteFailedVM"
        />
      </div>
    </div>

    <div v-if="markdownReport" class="deploy-report">
      <div class="deploy-report-head">
        <span>Dependency report</span>
        <Button label="Copy markdown" text size="small" icon="pi pi-copy" @click="copyReport" />
      </div>
      <pre class="deploy-report-body">{{ markdownReport }}</pre>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
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
const port = ref("");
const makePublic = ref(false);
const detectedPorts = ref<number[]>([]);
const useSavedKey = ref(false);
const maskedKey = ref("");
const dryRun = ref(false);
const starting = ref(false);
const running = ref(false);
const formError = ref("");
const events = ref<DeployEvent[]>([]);
const finished = ref<"" | "success" | "failed">("");
const failedVMName = ref(""); // set when a run fails after the VM was created
const deletingVM = ref(false);
const fullClone = ref(false);
const fullCloneSupported = ref(true);
const skipSystemd = ref(false);
const sourceOS = ref("");
const markdownReport = ref("");
const consoleRef = ref<HTMLElement | null>(null);
let es: EventSource | null = null;

const savedHint = ref("");

const vmNameError = computed(() => {
  const value = vmName.value.trim();
  if (!value) return "VM name is required.";
  if (value.length > 40) return "VM name must be 40 characters or fewer.";
  if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(value)) {
    return "Use lowercase letters, digits, and internal hyphens only.";
  }
  return "";
});

const imageError = computed(() => {
  const value = image.value.trim();
  if (!value) return "";
  if (value.startsWith("-") || /\s/.test(value)) return "Image must be one container image reference without whitespace.";
  return "";
});

const projectDirError = computed(() => {
  const value = projectDir.value.trim();
  if (!value) return "Project directory is required.";
  if (!value.startsWith("/")) return "Project directory must be an absolute path.";
  if (value === "/") return "The filesystem root cannot be deployed.";
  return "";
});

const apiKeyError = computed(() => {
  if (useSavedKey.value) return maskedKey.value ? "" : "An exe.dev API key is required.";
  const value = apiKey.value.trim();
  if (!value) return "An exe.dev API key is required.";
  if (!value.startsWith("exe") || !value.includes(".") || /\s/.test(value)) return "Enter a valid exe.dev API token.";
  return "";
});

const portError = computed(() => {
  const value = port.value.trim();
  if (!value) return "";
  if (!/^\d+$/.test(value)) return "App port must contain digits only.";
  const parsed = Number(value);
  if (parsed < 3000 || parsed > 9999) return "App port must be between 3000 and 9999.";
  return "";
});

const formValid = computed(
  () => !vmNameError.value && !imageError.value && !projectDirError.value && !apiKeyError.value && !portError.value,
);

const portHint = computed(() => {
  const value = port.value.trim();
  if (!value) {
    return "Leave blank to skip proxy routing and service reconfiguration.";
  }
  if (!/^\d+$/.test(value)) return "Enter a whole-number port from 3000 to 9999.";
  const p = Number(value);
  if (p === 8000) return "Port 8000 is the exe.dev default proxy target.";
  if (p >= 3000 && p <= 9999) return `The new VM's URL will be https://name.exe.xyz:${p}/`;
  return "Ports 3000–9999 are supported by the exe.dev proxy.";
});

watch(
  () => props.isOpen,
  async (open) => {
    if (!open) return;
    try {
      const s = await deployApi.getSettings();
      maskedKey.value = s.api_key_masked;
      useSavedKey.value = Boolean(s.api_key_masked);
      image.value = s.default_image;
      if (!projectDir.value && props.suggestedDir) projectDir.value = props.suggestedDir;
      if (s.detected_app_ports?.length) {
        detectedPorts.value = s.detected_app_ports;
        if (!port.value) port.value = String(s.detected_app_ports[0]);
      }
      sourceOS.value = s.source_os ?? "";
      fullCloneSupported.value = s.full_clone_supported ?? false;
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
  if (starting.value || running.value) return;
  formError.value = "";
  if (!formValid.value) {
    formError.value = "Complete the required fields and correct the highlighted values.";
    return;
  }
  starting.value = true;
  events.value = [];
  finished.value = "";
  failedVMName.value = "";
  markdownReport.value = "";
  try {
    // If the user typed a new key, save it first so future deploys reuse it.
    if (!useSavedKey.value) {
      await deployApi.putSettings(apiKey.value.trim());
      useSavedKey.value = true;
      const s = await deployApi.getSettings();
      maskedKey.value = s.api_key_masked;
    }
    const p = parseDeployPort(port.value);
    await deployApi.start({
      vm_name: vmName.value.trim(),
      image: image.value.trim(),
      project_dir: projectDir.value.trim(),
      port: p,
      make_public: makePublic.value,
      dry_run: dryRun.value,
      full_clone: fullCloneSupported.value && fullClone.value,
      skip_systemd: skipSystemd.value,
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
        if (data.markdown_report) markdownReport.value = data.markdown_report;
        if (data.status === "failed" && data.error) {
          const message = String(data.error);
          const alreadyShown = events.value.some((event) => event.level === "error" && event.message === message);
          if (!alreadyShown) {
            events.value.push({
              time: new Date().toISOString(),
              level: "error",
              step: "deploy",
              message,
            });
          }
        }
        if (data.status === "failed" && data.vm_created) {
          failedVMName.value = data.vm_name ?? "";
        }
        running.value = false;
        es?.close();
        scrollConsole();
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
  failedVMName.value = "";
}

async function deleteFailedVM() {
  if (!failedVMName.value) return;
  deletingVM.value = true;
  try {
    await deployApi.deleteVM(failedVMName.value);
    events.value.push({
      time: new Date().toISOString(),
      level: "success",
      step: "cleanup",
      message: `VM "${failedVMName.value}" deleted.`,
    });
    failedVMName.value = "";
  } catch (err) {
    events.value.push({
      time: new Date().toISOString(),
      level: "error",
      step: "cleanup",
      message: String(err instanceof Error ? err.message : err),
    });
  } finally {
    deletingVM.value = false;
    scrollConsole();
  }
}

function scrollConsole() {
  nextTick(() => {
    if (consoleRef.value) consoleRef.value.scrollTop = consoleRef.value.scrollHeight;
  });
}

function parseDeployPort(value: string): number | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  if (!/^\d+$/.test(trimmed)) {
    throw new Error("App port must contain digits only.");
  }
  const parsed = Number(trimmed);
  if (parsed < 3000 || parsed > 9999) {
    throw new Error("App port must be between 3000 and 9999.");
  }
  return parsed;
}

async function copyDryRun() {
  const lines = events.value.map((event) => `${shortTime(event.time)} [${event.step}] ${event.message}`);
  if (finished.value === "success") lines.push("✅ Deploy finished");
  if (finished.value === "failed") lines.push("❌ Deploy failed");
  await navigator.clipboard.writeText(lines.join("\n"));
}

async function copyReport() {
  try {
    await navigator.clipboard.writeText(markdownReport.value);
  } catch {
    // clipboard may be blocked; user can still select from the <pre>
  }
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
.deploy-field-row {
  display: flex;
  gap: 1rem;
  align-items: flex-start;
}
.deploy-field-row .deploy-field {
  flex: 1;
}
.deploy-public {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.85rem;
  font-weight: 600;
  margin-top: 1.4rem;
  white-space: nowrap;
}
.deploy-field label {
  font-weight: 600;
  font-size: 0.85rem;
}
.deploy-hint {
  color: var(--p-text-muted-color, #888);
  font-size: 0.78rem;
}
.deploy-field-error,
.deploy-required {
  color: var(--p-red-500, #ef4444);
}
.deploy-field-error {
  font-size: 0.78rem;
}
.deploy-optional {
  color: var(--p-text-muted-color, #888);
  font-size: 0.75rem;
  font-weight: 400;
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
  flex-wrap: wrap;
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
/* Dark mode: ensure the console is visible on Android Chrome dark mode. */
.dark .deploy-console {
  background: #1a1a1a;
  color: #d4d4d4;
}
/* Light mode: use a dark console even in light theme (terminal aesthetic). */
.deploy-console {
  background: #1a1a1a;
  color: #d4d4d4;
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
.deploy-disabled {
  opacity: 0.5;
}
.deploy-warn-hint {
  color: var(--p-yellow-500, #eab308);
}
.deploy-report {
  margin-top: 0.75rem;
}
.deploy-report-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-weight: 600;
  font-size: 0.85rem;
  margin-bottom: 0.25rem;
}
.deploy-report-body {
  max-height: 260px;
  overflow: auto;
  border-radius: 6px;
  padding: 0.6rem;
  font-size: 0.75rem;
  white-space: pre-wrap;
}
/* Light mode: light background with dark text. */
.deploy-report-body {
  background: var(--p-surface-100, #f5f5f5);
  color: var(--p-text-color, #1f2937);
}
/* Dark mode: dark background with light text. Use .dark (not html.dark-mode). */
.dark .deploy-report-body {
  background: var(--p-surface-800, #27272a);
  color: var(--p-text-color, #f9fafb);
}
.deploy-delete-btn {
  margin-left: 0.75rem;
}
.deploy-cmd .deploy-msg {
  color: #93c5fd;
}
</style>
