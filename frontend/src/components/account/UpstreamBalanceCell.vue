<template>
  <div v-if="account.type === 'apikey'" class="flex h-6 min-w-[9rem] items-center gap-1" data-testid="upstream-balance-cell">
    <HelpTooltip width-class="w-72">
      <template #trigger>
        <span class="cursor-help border-b border-dotted text-sm tabular-nums" :class="statusClass" data-testid="upstream-balance-value">{{ value }}</span>
      </template>
      <div class="space-y-1">
        <p>{{ statusLabel }}</p>
        <p v-if="snapshot?.source">{{ snapshot.provider === 'new_api' ? 'New API' : 'Sub2API' }} · {{ snapshot.source }}</p>
        <p v-if="snapshot?.received_at">{{ t('admin.accounts.upstreamBalance.updatedAt', { value: date(snapshot.received_at) }) }}</p>
        <p v-if="snapshot?.next_query_at">{{ t('admin.accounts.upstreamBalance.nextQueryAt', { value: date(snapshot.next_query_at) }) }}</p>
        <p v-if="snapshot?.last_error">{{ t(`admin.accounts.upstreamBalance.errors.${snapshot.last_error}`) }}</p>
      </div>
    </HelpTooltip>
    <button type="button" class="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-blue-600 hover:bg-blue-50 disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
      :disabled="querying" :aria-label="t('admin.accounts.upstreamBalance.refresh')" :title="t('admin.accounts.upstreamBalance.refresh')" @click="$emit('query')">
      <Icon name="refresh" size="xs" :class="{ 'animate-spin': querying }" />
    </button>
  </div>
  <span v-else class="text-sm text-gray-400">--</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Account } from '@/types'

const props = defineProps<{ account: Account; now: number; querying?: boolean }>()
defineEmits<{ (event: 'query'): void }>()
const { t, locale } = useI18n()
const snapshot = computed(() => props.account.extra?.upstream_balance)
const stale = computed(() => snapshot.value?.fresh_until ? props.now > Date.parse(snapshot.value.fresh_until) : false)
const status = computed(() => stale.value ? 'stale' : snapshot.value?.status ?? 'pending')
const statusLabel = computed(() => t(`admin.accounts.upstreamBalance.status.${status.value}`))
const statusClass = computed(() => status.value === 'failed' ? 'text-red-600 dark:text-red-400' : ['stale', 'unconfirmed'].includes(status.value) ? 'text-amber-600 dark:text-amber-400' : 'text-gray-700 dark:text-gray-200')
const value = computed(() => {
  const data = snapshot.value
  if (!data || data.balance == null || !Number.isFinite(data.balance)) return data ? statusLabel.value : '--'
  const formatted = new Intl.NumberFormat(locale.value, { maximumFractionDigits: Math.abs(data.balance) < 0.01 ? 8 : 2 }).format(data.balance)
  const amount = `${formatted}${data.currency ? ` ${data.currency}` : ''}`
  return data.scope === 'wallet' ? amount : `${amount} *`
})
const date = (value: string) => new Date(value).toLocaleString(locale.value, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
</script>
