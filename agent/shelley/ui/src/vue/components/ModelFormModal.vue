<!-- Add / edit dialog for a custom model. Renders as its own Modal (a stacked
     PrimeVue Dialog) layered on top of ModelsModal's list, so it has a proper
     title + close button and the list stays visible/unchanged underneath
     rather than being swapped out. Owns the whole form + test/save lifecycle;
     emits `saved` (parent reloads the list) and `close`. `editModel` is the
     custom model being edited, or null when adding. -->
<template>
  <Modal
    :is-open="isOpen"
    :title="editModel ? t('editModel') : t('addModel')"
    class-name="modal-wide"
    @close="emit('close')"
  >
    <div class="model-form">
      <div v-if="error" class="models-error">
        {{ error }}
        <button class="models-error-dismiss" @click="error = null">×</button>
      </div>

      <!-- Provider Selection -->
      <div class="form-group">
        <label>{{ t("providerApiFormat") }}</label>
        <div class="provider-buttons">
          <button
            v-for="p in providerTypes"
            :key="p"
            type="button"
            :class="`provider-btn ${form.provider_type === p ? 'selected' : ''}`"
            @click="handleProviderChange(p)"
          >
            {{ PROVIDER_LABELS[p] }}
          </button>
        </div>
      </div>

      <!-- Endpoint Selection -->
      <div class="form-group">
        <label>{{ t("endpoint") }}</label>
        <div class="endpoint-toggle">
          <button
            type="button"
            :class="`toggle-btn ${!form.endpoint_custom ? 'selected' : ''}`"
            @click="handleEndpointModeChange(false)"
          >
            {{ t("defaultEndpoint") }}
          </button>
          <button
            type="button"
            :class="`toggle-btn ${form.endpoint_custom ? 'selected' : ''}`"
            @click="handleEndpointModeChange(true)"
          >
            {{ t("customEndpoint") }}
          </button>
        </div>
        <InputText
          v-if="form.endpoint_custom"
          v-model="form.endpoint"
          placeholder="https://..."
          fluid
          :dt="inputFieldDt"
        />
        <div v-else class="endpoint-display">{{ form.endpoint }}</div>
      </div>

      <!-- Model Name with autocomplete suggestions -->
      <div class="form-group">
        <label>{{ t("model") }}</label>
        <InputText
          :model-value="form.model_name"
          placeholder="Model name (e.g., claude-sonnet-4-6)"
          fluid
          :dt="inputFieldDt"
          :list="`model-name-suggestions-${form.provider_type}`"
          autocomplete="off"
          @update:model-value="onModelNameInput($event ?? '')"
        />
        <datalist :id="`model-name-suggestions-${form.provider_type}`">
          <option
            v-for="preset in DEFAULT_MODELS[form.provider_type]"
            :key="preset.model_name"
            :value="preset.model_name"
          >
            {{ preset.name }}
          </option>
        </datalist>
      </div>

      <!-- Display Name -->
      <div class="form-group">
        <label>{{ t("displayName") }}</label>
        <InputText
          v-model="form.display_name"
          :placeholder="t('nameShownInSelector')"
          fluid
          :dt="inputFieldDt"
        />
      </div>

      <!-- API Key -->
      <div class="form-group">
        <label>{{ t("apiKey") }}</label>
        <InputText
          v-model="form.api_key"
          :placeholder="t('enterApiKey')"
          fluid
          :dt="inputFieldDt"
          autocomplete="off"
        />
      </div>

      <!-- Max Tokens -->
      <div class="form-group">
        <label>{{ t("maxContextTokens") }}</label>
        <InputText
          type="number"
          :model-value="String(form.max_tokens)"
          fluid
          :dt="inputFieldDt"
          @update:model-value="form.max_tokens = parseInt($event ?? '') || 200000"
        />
      </div>

      <!-- Image input support -->
      <div class="form-group">
        <label>{{ t("imageSupport") }}</label>
        <Select
          v-model="form.image_support"
          :options="imageSupportOptions"
          option-label="label"
          option-value="value"
          fluid
          :dt="selectFieldDt"
        />
        <div class="form-hint">{{ t("imageSupportHelp") }}</div>
        <div v-if="editingResolvedAuto" class="form-hint">
          <code>auto({{ editingResolvedAuto.endpoint }}, {{ editingResolvedAuto.model }})</code>
          {{ t("imageSupportAutoResolved") }}
          {{ editingResolvedAuto.supported ? t("imageSupportYes") : t("imageSupportNo") }}
        </div>
      </div>

      <!-- Legacy provider default for OpenAI Responses models -->
      <div v-if="form.provider_type === 'openai-responses'" class="form-group">
        <label>{{ t("reasoningEffort") }}</label>
        <InputText
          v-model="form.reasoning_effort"
          :placeholder="t('reasoningEffortPlaceholder')"
          fluid
          :dt="inputFieldDt"
          list="reasoning-effort-suggestions"
          autocomplete="off"
        />
        <datalist id="reasoning-effort-suggestions">
          <option
            v-for="suggestion in REASONING_EFFORT_SUGGESTIONS"
            :key="suggestion"
            :value="suggestion"
          />
        </datalist>
        <div class="form-hint">{{ t("reasoningEffortHint") }}</div>
      </div>

      <!-- Reasoning capability and generic level mapping -->
      <div class="form-group">
        <label>{{ t("supportsReasoning") }}</label>
        <Select
          v-model="form.reasoning_support"
          :options="reasoningSupportOptions"
          option-label="label"
          option-value="value"
          fluid
          :dt="selectFieldDt"
        />
        <div class="form-hint">
          {{ t("reasoningSupportHelp") }}
        </div>
      </div>
      <div v-if="form.reasoning_support !== 'no'" class="form-group">
        <label>{{ t("reasoningLevelMapping") }}</label>
        <div class="reasoning-map-grid">
          <label v-for="level in REASONING_LEVELS" :key="level" class="reasoning-map-row">
            <span>{{ level }}</span>
            <span class="reasoning-map-arrow" aria-hidden="true">→</span>
            <InputText
              v-model="form.reasoning_map[level]"
              class="reasoning-map-input"
              fluid
              :dt="inputFieldDt"
              :list="`reasoning-map-${level}`"
              :placeholder="t('reasoningMappingUnsupported')"
            />
            <datalist :id="`reasoning-map-${level}`">
              <option v-for="target in REASONING_LEVELS" :key="target" :value="target" />
              <option value="none" />
            </datalist>
          </label>
        </div>
        <div class="form-hint">
          {{ t("reasoningMappingHelp") }}
        </div>
      </div>

      <!-- Tags -->
      <div class="form-group">
        <label>
          {{ t("tags") }}
          <span
            class="info-icon-wrapper"
            v-tooltip.top="t('tagsTooltip')"
            tabindex="0"
            role="note"
            :aria-label="t('tagsTooltip')"
          >
            <span class="info-icon">
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="14" height="14">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                />
              </svg>
            </span>
          </span>
        </label>
        <InputText
          v-model="form.tags"
          :placeholder="t('tagsPlaceholder')"
          fluid
          :dt="inputFieldDt"
        />
      </div>

      <!-- Test Result -->
      <div v-if="testResult" :class="`test-result ${testResult.success ? 'success' : 'error'}`">
        {{ testResult.success ? "✓" : "✗" }} {{ testResult.message }}
      </div>

      <!-- Form Actions -->
      <div class="form-actions">
        <Button type="button" severity="secondary" :label="t('cancel')" @click="emit('close')" />
        <Button
          type="button"
          severity="secondary"
          :label="testing ? t('testingButton') : t('testButton')"
          :disabled="testing || (!form.api_key && !editModel) || !form.model_name"
          :title="
            !form.model_name
              ? 'Enter model name to test'
              : !form.api_key && !editModel
                ? 'Enter API key to test'
                : ''
          "
          @click="handleTest"
        />
        <Button
          type="button"
          :label="editModel ? t('save') : t('addModel')"
          :disabled="!form.display_name || !form.api_key || !form.model_name"
          @click="handleSave"
        />
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Select from "primevue/select";
import { inputFieldDt, selectFieldDt } from "./configFieldDt";
import Modal from "./Modal.vue";
import { useI18n } from "../composables/i18n";
import {
  customModelsApi,
  type CustomModel,
  type CreateCustomModelRequest,
  type TestCustomModelRequest,
} from "../../services/api";
import {
  DEFAULT_ENDPOINTS,
  DEFAULT_MODELS,
  DEFAULT_REASONING_MAP,
  PROVIDER_LABELS,
  REASONING_EFFORT_SUGGESTIONS,
  REASONING_LEVELS,
  emptyForm,
  providerTypes,
  type FormData,
  type ProviderType,
} from "./customModelConstants";

const props = defineProps<{ isOpen: boolean; editModel: CustomModel | null }>();
const emit = defineEmits<{ (e: "close"): void; (e: "saved"): void }>();

const { t } = useI18n();

const imageSupportOptions = computed(() => [
  { label: t("imageSupportAuto"), value: "auto" },
  { label: t("imageSupportYes"), value: "yes" },
  { label: t("imageSupportNo"), value: "no" },
]);
const reasoningSupportOptions = computed(() => [
  { label: t("reasoningSupportAuto"), value: "auto" },
  { label: t("reasoningSupportYes"), value: "yes" },
  { label: t("reasoningSupportNo"), value: "no" },
]);

const form = reactive<FormData>({ ...emptyForm });
const error = ref<string | null>(null);
const testing = ref(false);
const testResult = ref<{ success: boolean; message: string } | null>(null);

function resetForm() {
  Object.assign(form, emptyForm, { reasoning_map: { ...DEFAULT_REASONING_MAP } });
}

function serializeReasoningMap(): string {
  return JSON.stringify(
    Object.fromEntries(
      REASONING_LEVELS.flatMap((level) => {
        const target = form.reasoning_map[level];
        return target ? [[level, target]] : [];
      }),
    ),
  );
}

function parseReasoningMap(raw: string) {
  const result = { ...DEFAULT_REASONING_MAP };
  if (!raw) return result;
  try {
    const parsed = JSON.parse(raw) as Record<string, string>;
    for (const level of REASONING_LEVELS) result[level] = parsed[level] || "";
  } catch {
    // Invalid stored mappings are rejected by the server; keep safe defaults.
  }
  return result;
}

// When editing a model whose image support is Auto, surface what auto() would
// resolve to (from the model's stored endpoint/model + server verdict).
const editingResolvedAuto = computed(() => {
  if (form.image_support !== "auto" || !props.editModel) return null;
  return {
    endpoint: props.editModel.endpoint || "\u2014",
    model: props.editModel.model_name || "\u2014",
    supported: props.editModel.supports_images ?? true,
  };
});

// Populate the form from editModel (or reset to blank for add) each time the
// dialog opens, and clear any stale error/test state.
watch(
  () => props.isOpen,
  (open) => {
    if (!open) return;
    error.value = null;
    testResult.value = null;
    const m = props.editModel;
    if (m) {
      Object.assign(form, {
        display_name: m.display_name,
        provider_type: m.provider_type,
        endpoint: m.endpoint,
        endpoint_custom: m.endpoint !== DEFAULT_ENDPOINTS[m.provider_type as ProviderType],
        api_key: m.api_key,
        model_name: m.model_name,
        max_tokens: m.max_tokens,
        tags: m.tags,
        reasoning_effort: m.reasoning_effort || "",
        reasoning_support: m.reasoning_support || "auto",
        reasoning_map: parseReasoningMap(m.reasoning_map),
        image_support: m.image_support ?? "auto",
      });
    } else {
      resetForm();
    }
  },
  { immediate: true },
);

function handleProviderChange(provider: ProviderType) {
  form.provider_type = provider;
  form.endpoint = form.endpoint_custom ? form.endpoint : DEFAULT_ENDPOINTS[provider];
}

function handleEndpointModeChange(custom: boolean) {
  form.endpoint_custom = custom;
  form.endpoint = custom ? form.endpoint : DEFAULT_ENDPOINTS[form.provider_type];
}

function onModelNameInput(v: string) {
  const preset = DEFAULT_MODELS[form.provider_type].find((p) => p.model_name === v);
  form.model_name = v;
  if (preset && !form.display_name) form.display_name = preset.name;
}

async function handleTest() {
  if (!form.model_name) {
    testResult.value = { success: false, message: t("modelNameRequired") };
    return;
  }
  if (!form.api_key && !props.editModel) {
    testResult.value = { success: false, message: t("apiKeyRequired") };
    return;
  }
  testing.value = true;
  testResult.value = null;
  try {
    const request: TestCustomModelRequest = {
      model_id: props.editModel?.model_id || undefined,
      provider_type: form.provider_type,
      endpoint: form.endpoint,
      api_key: form.api_key,
      model_name: form.model_name,
      reasoning_effort: form.reasoning_effort,
      reasoning_support: form.reasoning_support,
      reasoning_map: serializeReasoningMap(),
    };
    testResult.value = await customModelsApi.testCustomModel(request);
  } catch (err) {
    testResult.value = {
      success: false,
      message: err instanceof Error ? err.message : "Test failed",
    };
  } finally {
    testing.value = false;
  }
}

async function handleSave() {
  if (!form.display_name || !form.api_key || !form.model_name) {
    error.value = "Display name, API key, and model name are required";
    return;
  }
  try {
    error.value = null;
    const request: CreateCustomModelRequest = {
      display_name: form.display_name,
      provider_type: form.provider_type,
      endpoint: form.endpoint,
      api_key: form.api_key,
      model_name: form.model_name,
      max_tokens: form.max_tokens,
      tags: form.tags,
      reasoning_effort: form.reasoning_effort,
      reasoning_support: form.reasoning_support,
      reasoning_map: serializeReasoningMap(),
      image_support: form.image_support,
    };
    if (props.editModel) {
      await customModelsApi.updateCustomModel(props.editModel.model_id, request);
    } else {
      await customModelsApi.createCustomModel(request);
    }
    emit("saved");
    emit("close");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to save model";
  }
}
</script>
