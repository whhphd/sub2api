<template>
  <div v-if="eligible" class="mt-2 space-y-1 text-xs text-gray-500 dark:text-gray-400" data-testid="turn-state-cell">
    <div class="font-medium">Turn-State · {{ t(`admin.accounts.turnState.${status}`) }}</div>
    <div v-if="account.parent_account_id">{{ t('admin.accounts.turnState.parent', { id: account.parent_account_id }) }}</div>
    <template v-else>
      <div v-if="observed" :title="observedTime">
        {{ observed.model }} · {{ observed.length }} / {{ observed.blocks }}
        · {{ t(`admin.accounts.turnState.${observed.shape === 'baseline' ? 'baseline' : 'nonBaseline'}`) }}
        <div>{{ observedTime }}</div>
      </div>
      <div v-for="item in models" :key="item.model">
        {{ item.model }} · {{ t('admin.accounts.turnState.available', { count: item.available }) }}
        <span v-if="item.expires"> · {{ new Date(item.expires).toLocaleString() }}</span>
      </div>
      <div class="text-[10px]">{{ t('admin.accounts.turnState.hint') }}</div>
    </template>
  </div>
</template>

<script setup lang="ts">
// Presentation adapted from KlN v0.2.5-klno.9 AccountTurnStateCell.
// CallAI consumes server-redacted metadata only, never opaque state blobs.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'

const props = defineProps<{ account: Account; enabled: boolean | null; now: number }>()
const { t } = useI18n()
const eligible = computed(() => props.account.platform === 'openai' && props.account.type === 'oauth')
interface Observation { model: string; length: number; blocks: number; shape: string; observed_at: string }
interface Candidate { model: string; expires_at: string; failed: boolean }
const observed = computed(() => props.account.extra?.openai_turn_state_observed as Observation | undefined)
const observedTime = computed(() => observed.value?.observed_at ? new Date(observed.value.observed_at).toLocaleString() : '--')
const candidates = computed(() => {
  const summary = props.account.extra?.openai_turn_state_summary as { candidates?: Candidate[] } | undefined
  return Array.isArray(summary?.candidates) ? summary.candidates : []
})
const models = computed(() => {
  const groups = new Map<string, { model: string; available: number; expires: string }>()
  for (const candidate of candidates.value) {
    const entry = groups.get(candidate.model) ?? { model: candidate.model, available: 0, expires: '' }
    if (!candidate.failed && Date.parse(candidate.expires_at) > props.now) {
      entry.available++
      if (!entry.expires || candidate.expires_at > entry.expires) entry.expires = candidate.expires_at
    }
    groups.set(candidate.model, entry)
  }
  return [...groups.values()]
})
const status = computed(() => {
  if (props.account.parent_account_id) return 'inherited'
  if (props.enabled === null) return 'unknown'
  if (!props.enabled) return 'observeOnly'
  if (models.value.some(item => item.available > 0)) return 'ready'
  if (!candidates.value.length) return 'waiting'
  return candidates.value.every(item => item.failed) ? 'rejected' : 'expired'
})
</script>
