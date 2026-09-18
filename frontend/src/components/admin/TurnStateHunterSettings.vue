<template>
  <div class="card space-y-4 p-6" data-testid="turn-state-hunter-settings">
    <div><h3 class="font-medium">{{ t('admin.settings.turnStateHunter.title') }}</h3><p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.turnStateHunter.description') }}</p></div>
    <p v-if="loadError" role="alert" class="text-sm text-red-500">{{ loadError }} <button type="button" class="underline" @click="load">{{ t('common.refresh') }}</button></p>
    <p v-if="!autoEnabled && !loading" class="text-sm text-amber-600 dark:text-amber-400">{{ t('admin.settings.turnStateHunter.needsAuto') }}</p>
    <fieldset :disabled="loading || saving || !!loadError" class="space-y-4 disabled:opacity-60">
      <label class="flex items-center justify-between gap-4"><span>{{ t('admin.settings.turnStateHunter.enabled') }}</span><Toggle v-model="form.enabled" data-testid="hunter-enabled" /></label>
      <label class="block text-sm">{{ t('admin.settings.turnStateHunter.models') }}<input v-model="modelsText" class="input mt-1 w-full" :placeholder="t('admin.settings.turnStateHunter.modelsPlaceholder')" data-testid="hunter-models" /></label>
      <div class="text-sm">
        <label for="hunter-proxy-filter">{{ t('admin.settings.turnStateHunter.proxies') }}</label>
        <input id="hunter-proxy-filter" v-model="proxySearch" class="input mt-1 w-full" :placeholder="t('admin.settings.turnStateHunter.searchProxies')" />
        <div class="mt-2 max-h-40 space-y-1 overflow-auto rounded border border-gray-200 p-2 dark:border-dark-600">
          <label v-for="p in filteredProxies" :key="p.id" class="flex items-center gap-2 py-1"><input v-model="form.proxy_ids" type="checkbox" :value="p.id" :disabled="p.status !== 'active' && !form.proxy_ids.includes(p.id)" /><span class="break-all">{{ p.name }} <span class="text-gray-500">{{ p.host }}:{{ p.port }}</span></span></label>
          <p v-if="!filteredProxies.length" class="text-gray-500">{{ t('admin.settings.turnStateHunter.noProxies') }}</p>
        </div>
        <p class="mt-1 text-xs text-gray-500">{{ t('admin.settings.turnStateHunter.proxyHint') }}</p>
        <p v-if="missingProxyIDs.length" class="text-amber-600">{{ t('admin.settings.turnStateHunter.missingProxies', { ids: missingProxyIDs.join(', ') }) }}</p>
      </div>
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label v-for="field in numericFields" :key="field.key" class="text-sm">{{ t(`admin.settings.turnStateHunter.${field.key}`) }}<input v-model.number="form[field.key]" class="input mt-1 w-full" type="number" :min="field.min" :max="field.max" step="1" :data-testid="`hunter-${field.key}`" /></label>
        <label class="text-sm">{{ t('admin.settings.turnStateHunter.effort') }}<select v-model="form.reasoning_effort" class="input mt-1 w-full"><option v-for="v in ['minimal', 'low', 'medium', 'high', 'xhigh']" :key="v" :value="v">{{ v }}</option></select></label>
      </div>
      <div class="flex justify-end"><button type="button" class="btn btn-primary" data-testid="hunter-save" @click="save">{{ saving ? t('common.saving') : t('common.save') }}</button></div>
    </fieldset>
  </div>
</template>
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
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
const form = reactive<TurnStateHunterConfig>({ enabled: false, models: [], proxy_ids: [], max_per_hour: 300, per_account_max_per_hour: 30, gap_seconds: 20, lead_minutes: 10, retry_minutes: 10, idle_minutes: 60, reasoning_effort: 'high' })
const numericFields = [
  { key: 'max_per_hour', min: 1, max: undefined }, { key: 'per_account_max_per_hour', min: 1, max: undefined },
  { key: 'gap_seconds', min: 1, max: 600 }, { key: 'lead_minutes', min: 1, max: 55 },
  { key: 'retry_minutes', min: 1, max: 1440 }, { key: 'idle_minutes', min: -1, max: 1440 },
] as const
const filteredProxies = computed(() => proxies.value.filter(p => `${p.name} ${p.host}`.toLowerCase().includes(proxySearch.value.toLowerCase())))
const missingProxyIDs = computed(() => form.proxy_ids.filter(id => !proxies.value.some(p => p.id === id)))
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
  if (models.length > 8 || form.proxy_ids.length > 64 || invalidNumbers || (form.enabled && (!models.length || !form.proxy_ids.length || missingProxyIDs.value.length))) {
    app.showError(t('admin.settings.turnStateHunter.invalid')); return
  }
  saving.value = true
  try {
    const result = await adminAPI.settings.updateOpenAIOAuthRuntimeSettings({ openai_oauth_turn_state_hunter: { ...form, models, proxy_ids: [...form.proxy_ids] } })
    if (result.openai_oauth_turn_state_hunter) Object.assign(form, result.openai_oauth_turn_state_hunter)
    modelsText.value = form.models.join(', '); autoEnabled.value = result.openai_oauth_turn_state_auto_enabled ?? false
    app.showSuccess(t('admin.settings.turnStateHunter.saved'))
  } catch (e) { app.showError(extractApiErrorMessage(e, t('admin.settings.turnStateHunter.saveFailed'))) }
  finally { saving.value = false }
}
onMounted(load)
</script>
