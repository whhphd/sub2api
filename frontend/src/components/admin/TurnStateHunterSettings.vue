<template>
  <div class="card space-y-4 p-6" data-testid="turn-state-hunter-settings">
    <div><h3 class="font-medium">{{ t('admin.settings.turnStateHunter.title') }}</h3><p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.turnStateHunter.description') }}</p></div>
    <p v-if="loadError" role="alert" class="text-sm text-red-500">{{ loadError }} <button type="button" class="underline" @click="load">{{ t('common.refresh') }}</button></p>
    <p v-if="!autoEnabled && !loading" class="text-sm text-amber-600 dark:text-amber-400">{{ t('admin.settings.turnStateHunter.needsAuto') }}</p>
    <fieldset :disabled="loading || saving || !!loadError" class="space-y-4 disabled:opacity-60">
      <label class="flex items-center justify-between gap-4"><span>{{ t('admin.settings.turnStateHunter.enabled') }}</span><Toggle v-model="form.enabled" data-testid="hunter-enabled" /></label>
      <label class="block text-sm">{{ t('admin.settings.turnStateHunter.models') }}<input v-model="modelsText" :disabled="form.auto_models" class="input mt-1 w-full" :placeholder="t('admin.settings.turnStateHunter.modelsPlaceholder')" data-testid="hunter-models" /></label>
      <label class="flex items-center gap-2 text-sm"><input v-model="form.auto_models" type="checkbox" data-testid="hunter-auto-models" />{{ t('admin.settings.turnStateHunter.autoModels') }}</label>
      <div class="text-sm">
        <label for="hunter-proxy-filter">{{ t('admin.settings.turnStateHunter.proxies') }}</label>
        <input id="hunter-proxy-filter" v-model="proxySearch" class="input mt-1 w-full" :placeholder="t('admin.settings.turnStateHunter.searchProxies')" />
        <div class="mt-2 max-h-40 space-y-1 overflow-auto rounded border border-gray-200 p-2 dark:border-dark-600">
          <label v-for="p in filteredProxies" :key="p.id" class="flex items-center gap-2 py-1"><input v-model="form.proxy_ids" type="checkbox" :value="p.id" :disabled="p.status !== 'active' && !form.proxy_ids.includes(p.id)" /><span class="break-all">{{ p.name }} <span class="text-gray-500">{{ p.host }}:{{ p.port }}</span></span></label>
          <p v-if="!filteredProxies.length" class="text-gray-500">{{ t('admin.settings.turnStateHunter.noProxies') }}</p>
        </div>
        <p class="mt-1 text-xs text-gray-500">{{ t('admin.settings.turnStateHunter.proxyHint') }}</p>
        <p v-if="missingProxyIDs.length" class="text-amber-600">{{ t('admin.settings.turnStateHunter.missingProxies', { ids: missingProxyIDs.join(', ') }) }}</p>
        <div v-if="selectedProxies.length" class="mt-2 space-y-1"><p class="text-xs text-gray-500">{{ t('admin.settings.turnStateHunter.rotatingProxies') }}</p><label v-for="p in selectedProxies" :key="p.id" class="flex items-center gap-2 text-xs"><input v-model="form.rotating_proxy_ids" type="checkbox" :value="p.id" :data-testid="`hunter-rotating-${p.id}`" /><span>{{ p.name }}</span></label></div>
      </div>
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label v-for="field in numericFields" :key="field.key" class="text-sm">{{ t(`admin.settings.turnStateHunter.${field.key}`) }}<input v-model.number="form[field.key]" class="input mt-1 w-full" type="number" :min="field.min" :max="field.max" step="1" :data-testid="`hunter-${field.key}`" /></label>
        <label class="text-sm">{{ t('admin.settings.turnStateHunter.effort') }}<select v-model="form.reasoning_effort" class="input mt-1 w-full"><option v-for="v in ['minimal', 'low', 'medium', 'high', 'xhigh']" :key="v" :value="v">{{ v }}</option></select></label>
        <label class="text-sm">{{ t('admin.settings.turnStateHunter.usageApiKey') }}<input v-model.number="form.usage_api_key_id" class="input mt-1 w-full" type="number" min="0" step="1" data-testid="hunter-usage-api-key" /></label>
      </div>
      <label class="flex items-center justify-between gap-4 text-sm"><span>{{ t('admin.settings.turnStateHunter.usageAccounting') }}</span><Toggle  :model-value="!!form.usage_accounting_enabled" @update:model-value="form.usage_accounting_enabled = $event" data-testid="hunter-usage-accounting" /></label>
      <label class="flex items-center justify-between gap-4 text-sm"><span>{{ t('admin.settings.turnStateHunter.holdWhenDegraded') }}</span><Toggle  :model-value="!!form.hold_when_degraded" @update:model-value="form.hold_when_degraded = $event" data-testid="hunter-hold-degraded" /></label>
      <p class="text-xs text-gray-500">{{ t('admin.settings.turnStateHunter.usageHint') }}</p>
      <p v-if="form.usage_accounting_enabled && !form.usage_api_key_id" role="status" class="text-sm text-amber-600">{{ t('admin.settings.turnStateHunter.usageMissing') }}</p>
      <p class="text-xs text-gray-500">{{ t('admin.settings.turnStateHunter.holdHint') }}</p>
      <div class="flex justify-end"><button type="button" class="btn btn-primary" data-testid="hunter-save" @click="save">{{ saving ? t('common.saving') : t('common.save') }}</button></div>
    </fieldset>
  </div>
</template>
<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import type { TurnStateHunterConfig } from '@/api/admin/settings'
import type { Proxy } from '@/types'
import Toggle from '@/components/common/Toggle.vue'
import { useAppStore } from '@/stores'
import { extractApiErrorMessage } from '@/utils/apiError'
const { t } = useI18n()
const app = useAppStore()
const loading = ref(true), saving = ref(false), loadError = ref(''), autoEnabled = ref(false)
const modelsText = ref(''), proxySearch = ref(''), proxies = ref<Proxy[]>([])
const form = reactive<TurnStateHunterConfig>({ enabled: false, models: [], proxy_ids: [], auto_models: false, rotating_proxy_ids: [], hold_when_degraded: false, usage_accounting_enabled: true, usage_api_key_id: 0, max_per_hour: 300, per_account_max_per_hour: 30, gap_seconds: 20, lead_minutes: 10, retry_minutes: 10, idle_minutes: 60, reasoning_effort: 'high' })
const numericFields = [
  { key: 'max_per_hour', min: 1, max: undefined }, { key: 'per_account_max_per_hour', min: 1, max: undefined },
  { key: 'gap_seconds', min: 1, max: 600 }, { key: 'lead_minutes', min: 1, max: 55 },
  { key: 'retry_minutes', min: 1, max: 1440 }, { key: 'idle_minutes', min: -1, max: 1440 },
] as const
const filteredProxies = computed(() => proxies.value.filter(p => `${p.name} ${p.host}`.toLowerCase().includes(proxySearch.value.toLowerCase())))
const missingProxyIDs = computed(() => form.proxy_ids.filter(id => !proxies.value.some(p => p.id === id)))
watch(() => [...form.proxy_ids], ids => { form.rotating_proxy_ids = (form.rotating_proxy_ids ?? []).filter(id => ids.includes(id)) })
const selectedProxies = computed(() => form.proxy_ids.map(id => proxies.value.find(p => p.id === id)).filter(Boolean) as Proxy[])
async function load() {
  loading.value = true; loadError.value = ''
  try {
    const [settings, list] = await Promise.all([adminAPI.settings.getOpenAIOAuthRuntimeSettings(), adminAPI.proxies.getAll()])
    autoEnabled.value = settings.openai_oauth_turn_state_auto_enabled ?? false
    if (settings.openai_oauth_turn_state_hunter) Object.assign(form, settings.openai_oauth_turn_state_hunter)
    modelsText.value = form.models.join(', '); proxies.value = list
  } catch (e) { loadError.value = extractApiErrorMessage(e, t('admin.settings.turnStateHunter.loadFailed')) }
  finally { loading.value = false }
}
async function save() {
  const models = [...new Set(modelsText.value.split(/[,，\n]/).map(v => v.trim().toLowerCase()).filter(Boolean))]
  const invalidNumbers = numericFields.some(f => !Number.isSafeInteger(form[f.key]) || form[f.key] < f.min || (f.max !== undefined && form[f.key] > f.max)) || form.idle_minutes === 0
  if (models.length > 8 || !Number.isSafeInteger(form.usage_api_key_id ?? 0) || (form.usage_api_key_id ?? 0) < 0 || form.proxy_ids.length > 64 || (form.rotating_proxy_ids ?? []).some(id => !form.proxy_ids.includes(id)) || invalidNumbers || (form.enabled && ((!models.length && !form.auto_models) || !form.proxy_ids.length || missingProxyIDs.value.length))) {
    app.showError(t('admin.settings.turnStateHunter.invalid')); return
  }
  saving.value = true
  try {
    const result = await adminAPI.settings.updateOpenAIOAuthRuntimeSettings({ openai_oauth_turn_state_hunter: { ...form, models, proxy_ids: [...form.proxy_ids], rotating_proxy_ids: [...(form.rotating_proxy_ids ?? [])] } })
    if (result.openai_oauth_turn_state_hunter) Object.assign(form, result.openai_oauth_turn_state_hunter)
    modelsText.value = form.models.join(', '); autoEnabled.value = result.openai_oauth_turn_state_auto_enabled ?? false
    app.showSuccess(result.turn_state_hold_release?.complete ? t('admin.settings.turnStateHunter.released', { count: result.turn_state_hold_release.released }) : t('admin.settings.turnStateHunter.saved'))
  } catch (e) { app.showError(extractApiErrorMessage(e, t('admin.settings.turnStateHunter.saveFailed'))) }
  finally { saving.value = false }
}
onMounted(load)
</script>
