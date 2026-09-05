<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { opsAPI } from '@/api/admin/ops'
import { opsNetworkAPI, type NetworkSettingsResponse } from '@/api/admin/opsNetwork'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: []; saved: [] }>()
const { t } = useI18n()
const data = ref<NetworkSettingsResponse | null>(null)
const loading = ref(false)
const saving = ref(false)
const adding = ref(false)
const error = ref('')
const presetsAdded = ref(false)
let loadSequence = 0
watch(() => props.show, async show => {
  const sequence = ++loadSequence
  if (!show) return
  loading.value = true
  error.value = ''
  presetsAdded.value = false
  try { const result = await opsNetworkAPI.settings(); if (sequence === loadSequence) data.value = result }
  catch { if (sequence === loadSequence) error.value = t('admin.ops.network.loadFailed') }
  finally { if (sequence === loadSequence) loading.value = false }
})
const deviceOptions = computed(() => {
  const names = new Set([...(data.value?.devices.map(d => d.name) ?? []), ...(data.value?.settings.links.map(l => l.device) ?? [])])
  return [...names].map(name => ({ value: name, label: name }))
})
const valid = computed(() => {
  const s = data.value?.settings
  return !!s && s.raw_retention_hours >= 1 && s.raw_retention_hours <= 24 && s.minute_retention_days >= 1 && s.minute_retention_days <= 365 && s.hourly_retention_days >= s.minute_retention_days && s.hourly_retention_days <= 730 && s.links.every(l => !!l.device && [l.rx_capacity_mbps, l.tx_capacity_mbps].every(v => Number.isFinite(v) && v > 0 && v <= 1000000)) && new Set(s.links.filter(l => l.enabled).map(l => l.device)).size === s.links.filter(l => l.enabled).length
})
async function save() {
  if (!data.value || !valid.value) return
  saving.value = true; error.value = ''
  try { data.value = await opsNetworkAPI.updateSettings(data.value.settings); emit('saved'); emit('close') }
  catch { error.value = t('admin.ops.network.saveFailed') }
  finally { saving.value = false }
}
async function addPresets() {
  if (!data.value) return
  adding.value = true; error.value = ''
  try {
    const rules = await opsAPI.listAlertRules()
    for (const preset of data.value.alert_presets) {
      if (rules.some(r => r.metric_type === preset.metric_type && r.threshold === preset.threshold && r.filters?.network_link === preset.filters?.network_link)) continue
      await opsAPI.createAlertRule({ ...preset, id: undefined, enabled: false, notify_email: false })
    }
    presetsAdded.value = true
  } catch { error.value = t('admin.ops.network.saveFailed') }
  finally { adding.value = false }
}
</script>

<template>
  <BaseDialog :show="show" :title="t('admin.ops.network.settings')" width="wide" @close="emit('close')">
    <div v-if="loading" class="py-8 text-center text-sm text-gray-500">{{ t('common.loading') }}</div>
    <div v-else-if="data" class="space-y-5">
      <div class="flex items-center justify-between gap-4"><span class="text-sm">{{ t('admin.ops.network.monitoring') }}</span><Toggle v-model="data.settings.enabled" :aria-label="t('admin.ops.network.monitoring')" /></div>
      <p v-if="!data.source_configured" class="text-sm text-amber-600">{{ t('admin.ops.network.states.unconfigured') }}</p>
      <fieldset v-for="link in data.settings.links" :key="link.id" class="border-t border-gray-200 pt-4 dark:border-dark-700">
        <legend class="text-sm font-semibold">{{ t(`admin.ops.network.${link.id}`) }}</legend>
        <div class="mt-2 flex items-center justify-between"><span class="text-xs text-gray-500">{{ t('admin.ops.network.enabled') }}</span><Toggle v-model="link.enabled" :aria-label="t(`admin.ops.network.${link.id}`)" /></div>
        <div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-3">
          <div><label class="input-label">{{ t('admin.ops.network.interface') }}</label><Select v-model="link.device" :options="deviceOptions" :aria-label="`${link.id} ${t('admin.ops.network.interface')}`" /></div>
          <label class="block"><span class="input-label">{{ t('admin.ops.network.rx') }} Mbps</span><input v-model.number="link.rx_capacity_mbps" class="input" type="number" min="1" max="1000000" :aria-label="`${link.id} rx Mbps`" /></label>
          <label class="block"><span class="input-label">{{ t('admin.ops.network.tx') }} Mbps</span><input v-model.number="link.tx_capacity_mbps" class="input" type="number" min="1" max="1000000" :aria-label="`${link.id} tx Mbps`" /></label>
        </div>
      </fieldset>
      <div class="grid grid-cols-1 gap-3 border-t border-gray-200 pt-4 dark:border-dark-700 sm:grid-cols-3">
        <label><span class="input-label">{{ t('admin.ops.network.rawRetention') }}</span><input v-model.number="data.settings.raw_retention_hours" class="input" type="number" min="1" max="24" /></label>
        <label><span class="input-label">{{ t('admin.ops.network.minuteRetention') }}</span><input v-model.number="data.settings.minute_retention_days" class="input" type="number" min="1" max="365" /></label>
        <label><span class="input-label">{{ t('admin.ops.network.hourRetention') }}</span><input v-model.number="data.settings.hourly_retention_days" class="input" type="number" :min="data.settings.minute_retention_days" max="730" /></label>
      </div>
      <button type="button" class="btn btn-secondary" :disabled="adding || presetsAdded" @click="addPresets"><Icon :name="presetsAdded ? 'check' : 'plus'" size="sm" class="mr-2" />{{ presetsAdded ? t('admin.ops.network.presetsAdded') : t('admin.ops.network.addPresets') }}</button>
    </div>
    <p v-if="error" class="mt-3 text-sm text-red-600" role="alert">{{ error }}</p>
    <template #footer><div class="flex justify-end gap-2"><button class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="saving || loading || !valid" @click="save">{{ saving ? t('common.saving') : t('common.save') }}</button></div></template>
  </BaseDialog>
</template>
