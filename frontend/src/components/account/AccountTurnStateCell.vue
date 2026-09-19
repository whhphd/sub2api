<template>
  <div v-if="eligible" class="mt-1 text-xs" data-testid="turn-state-cell">
    <button
      ref="trigger"
      type="button"
      class="inline-flex h-6 max-w-full items-center gap-1.5 whitespace-nowrap rounded px-1 text-gray-500 outline-none hover:bg-gray-100 focus-visible:ring-2 focus-visible:ring-primary-400 dark:text-gray-400 dark:hover:bg-white/5"
      :aria-label="summaryLabel"
      :aria-expanded="open"
      aria-haspopup="dialog"
      data-testid="turn-state-trigger"
      @mouseenter="show"
      @mouseleave="scheduleClose"
      @focus="show"
      @blur="scheduleClose"
      @click.stop="togglePinned"
    >
      <span class="text-[10px] font-semibold tracking-wide">TS</span>
      <Icon :name="statusIcon" size="xs" :class="statusColor" aria-hidden="true" />
      <template v-if="!account.parent_account_id">
        <span
          class="font-mono text-[11px] tabular-nums"
          :class="observed ? (observed.shape === 'baseline' ? 'text-sky-600 dark:text-sky-400' : 'text-amber-600 dark:text-amber-400') : ''"
          data-testid="turn-state-length"
        >{{ observed?.length ?? '--' }}</span>
        <span v-if="models.length" class="ml-0.5 inline-flex items-center gap-0.5" aria-hidden="true">
          <span
            v-for="item in models.slice(0, 6)"
            :key="item.model"
            class="h-3 w-1.5 shrink-0 rounded-sm"
            :class="modelColor(item)"
            data-testid="turn-state-model-bar"
          />
          <span v-if="models.length > 6" class="text-[10px]">+{{ models.length - 6 }}</span>
        </span>
        <span v-if="models.length" class="text-[10px] tabular-nums" data-testid="turn-state-coverage">{{ availableModels }}/{{ models.length }}</span>
      </template>
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        ref="panel"
        role="dialog"
        aria-label="Turn-State"
        class="fixed z-[99999] overflow-y-auto overscroll-contain rounded-xl bg-gray-900 p-3 text-left text-xs text-white shadow-xl ring-1 ring-white/10 dark:bg-gray-800"
        :style="panelStyle"
        @mouseenter="cancelClose"
        @mouseleave="scheduleClose"
        @focusin="cancelClose"
        @focusout="scheduleClose"
      >
        <div class="flex items-center gap-2 border-b border-white/10 pb-2">
          <span class="font-semibold">Turn-State</span>
          <span class="text-gray-300">{{ modeLabel }}</span>
          <button type="button" class="ml-auto rounded p-1 text-gray-400 hover:bg-white/10 hover:text-white" :aria-label="t('common.close')" @click.stop="close">
            <Icon name="x" size="xs" />
          </button>
        </div>
        <p v-if="account.parent_account_id" class="pt-2 text-gray-300">{{ t('admin.accounts.turnState.parent', { id: account.parent_account_id }) }}</p>
        <template v-else>
          <p v-if="enabled" class="pt-2 text-gray-400">{{ t('admin.accounts.turnState.requestDecision') }}</p>
          <div v-if="hunt" class="space-y-1 border-b border-white/10 py-2 text-gray-300">
            <div>{{ t('admin.accounts.turnState.hunterSummary', { count: hunt.hour_count }) }}</div>
            <div>{{ t(`admin.accounts.turnState.hunterGate.${hunterGate}`) }}</div>
            <div v-if="hunt.next_at && Date.parse(hunt.next_at) > now">{{ t('admin.accounts.turnState.hunterNext', { time: formatTime(hunt.next_at) }) }}</div>
            <div v-if="hunt.last?.[0]">{{ hunt.last[0].model }} · {{ hunt.last[0].chars || '--' }} · HTTP {{ hunt.last[0].status }} · {{ formatTime(hunt.last[0].at) }}</div>
          </div>
          <div class="space-y-1 py-2">
            <div class="text-gray-400">{{ t('admin.accounts.turnState.latestObservation') }}</div>
            <template v-if="observed">
              <div class="break-all font-medium">{{ observed.model }}</div>
              <div>{{ observed.length }} / {{ observed.blocks }} · {{ t(`admin.accounts.turnState.${observed.shape === 'baseline' ? 'baseline' : 'nonBaseline'}`) }}</div>
              <div class="text-gray-400">{{ formatTime(observed.observed_at) }}</div>
            </template>
            <div v-else class="text-gray-400">--</div>
          </div>
          <div v-if="models.length" class="space-y-2 border-t border-white/10 pt-2">
            <div class="text-gray-400">{{ t('admin.accounts.turnState.coverage', { available: availableModels, total: models.length }) }}</div>
            <div v-for="item in models" :key="item.model" class="flex items-start gap-2">
              <span class="mt-1 h-3 w-1.5 shrink-0 rounded-sm" :class="modelColor(item)" aria-hidden="true" />
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center justify-between gap-x-2">
                  <span class="break-all">{{ item.model }}</span>
                  <span class="text-gray-300">{{ item.available ? t('admin.accounts.turnState.available', { count: item.available }) : t(`admin.accounts.turnState.${modelStatus(item)}`) }}</span>
                </div>
                <div v-if="item.expires" class="text-[11px] text-gray-400">{{ t('admin.accounts.turnState.expires', { time: formatTime(item.expires) }) }}</div>
              </div>
            </div>
          </div>
        </template>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
// Presentation adapted from KlN v0.2.5-klno.9 AccountTurnStateCell.
// CallAI consumes server-redacted metadata only, never opaque state blobs.
import { computed, onBeforeUnmount, ref, watch, type CSSProperties } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import Icon from '@/components/icons/Icon.vue'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ account: Account; enabled: boolean | null; now: number }>()
const { t } = useI18n()
const eligible = computed(() => props.account.platform === 'openai' && props.account.type === 'oauth')
interface Observation { model: string; length: number; blocks: number; shape: string; observed_at: string }
interface Candidate { model: string; expires_at: string; failed: boolean }
interface ModelSummary { model: string; available: number; total: number; failed: number; expires: string }
const observed = computed(() => props.account.extra?.openai_turn_state_observed as Observation | undefined)
const formatTime = (value: string) => formatDateTime(value)
interface HuntSummary { hour_count: number; gate?: string; next_at?: string; last?: { at: string; model: string; chars: number; status: number }[] }
const hunt = computed(() => props.account.extra?.openai_turn_state_hunt as HuntSummary | undefined)
const candidates = computed(() => {
  const summary = props.account.extra?.openai_turn_state_summary as { candidates?: Candidate[] } | undefined
  return Array.isArray(summary?.candidates) ? summary.candidates : []
})
const models = computed(() => {
  const groups = new Map<string, ModelSummary>()
  const entryFor = (model: string) => {
    if (!groups.has(model)) groups.set(model, { model, available: 0, total: 0, failed: 0, expires: '' })
    return groups.get(model)!
  }
  // Include the last observed model even if it has never acquired a candidate.
  if (observed.value?.model) entryFor(observed.value.model)
  for (const candidate of candidates.value) {
    const entry = entryFor(candidate.model)
    entry.total++
    if (candidate.failed) entry.failed++
    if (!candidate.failed && Date.parse(candidate.expires_at) > props.now) {
      entry.available++
      if (!entry.expires || Date.parse(candidate.expires_at) > Date.parse(entry.expires)) entry.expires = candidate.expires_at
    }
  }
  return [...groups.values()].sort((a, b) => a.model.localeCompare(b.model))
})
const availableModels = computed(() => models.value.filter(item => item.available > 0).length)
const status = computed(() => {
  if (props.account.parent_account_id) return 'inherited'
  if (props.enabled === null) return 'unknown'
  if (!props.enabled) return 'observeOnly'
  if (availableModels.value) return 'ready'
  if (!candidates.value.length) return 'waiting'
  return candidates.value.every(item => item.failed) ? 'rejected' : 'expired'
})
const modeLabel = computed(() => t(`admin.accounts.turnState.${props.account.parent_account_id ? 'inherited' : props.enabled === null ? 'unknown' : props.enabled ? 'autoEnabled' : 'autoDisabled'}`))
// Stored hunter gates are snapshots: do not keep claiming freshness after expiry.
const hunterGate = computed(() => {
  if (hunt.value?.gate === 'fresh' && candidates.value.length && !availableModels.value) return 'needsRefresh'
  const holds = props.account.extra?.openai_turn_state_model_holds as Record<string, string> | undefined
  if (hunt.value?.gate === 'idle' && Object.values(holds ?? {}).some(until => Date.parse(until) > props.now)) return 'held'
  return hunt.value?.gate || 'ready'
})
const summaryLabel = computed(() => [
  modeLabel.value,
  `Turn-State · ${t(`admin.accounts.turnState.${status.value}`)}`,
  props.account.parent_account_id
    ? t('admin.accounts.turnState.parent', { id: props.account.parent_account_id })
    : t('admin.accounts.turnState.coverage', { available: availableModels.value, total: models.value.length }),
  t('admin.accounts.turnState.viewDetails'),
].join(' · '))
const statusIcon = computed(() => ({ ready: 'checkCircle', waiting: 'clock', rejected: 'xCircle', expired: 'clock', observeOnly: 'eye', unknown: 'exclamationCircle', inherited: 'link' } as const)[status.value])
const statusColor = computed(() => {
  if (status.value === 'ready') return 'text-emerald-600 dark:text-emerald-400'
  if (status.value === 'rejected') return 'text-red-500 dark:text-red-400'
  if (status.value === 'waiting') return 'text-amber-600 dark:text-amber-400'
  return 'text-gray-400 dark:text-gray-500'
})
const modelStatus = (item: ModelSummary) => item.available ? 'ready' : !item.total ? 'waiting' : item.failed === item.total ? 'rejected' : 'expired'
const modelColor = (item: ModelSummary) => ({
  ready: 'bg-emerald-500 dark:bg-emerald-400',
  waiting: 'bg-amber-500 dark:bg-amber-400',
  rejected: 'bg-red-500 dark:bg-red-400',
  expired: 'bg-gray-400 dark:bg-gray-500',
})[modelStatus(item)]

// Match the recent-request panel interaction without expanding the table row.
const open = ref(false)
const trigger = ref<HTMLElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const panelStyle = ref<CSSProperties>({})
let pinned = false
let closeTimer: ReturnType<typeof setTimeout> | undefined
function cancelClose() {
  clearTimeout(closeTimer)
  closeTimer = undefined
}
function updatePosition() {
  if (!trigger.value?.isConnected) return
  const p = getFloatingPanelPosition(trigger.value.getBoundingClientRect(), window.innerWidth, window.innerHeight, { maxWidth: 340 })
  panelStyle.value = { top: p.top == null ? undefined : `${p.top}px`, bottom: p.bottom == null ? undefined : `${p.bottom}px`, left: `${p.left}px`, width: `${p.width}px`, maxHeight: `${p.maxHeight}px` }
}
function show() {
  cancelClose()
  open.value = true
  updatePosition()
}
function close() {
  cancelClose()
  open.value = false
  pinned = false
}
function togglePinned() {
  if (pinned) close()
  else { show(); pinned = true }
}
function scheduleClose() {
  if (pinned) return
  cancelClose()
  closeTimer = setTimeout(close, 150)
}
function onOutsideClick(event: MouseEvent) {
  if (event.target instanceof Node && !trigger.value?.contains(event.target) && !panel.value?.contains(event.target)) close()
}
function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') close()
}
watch(open, (value, _, onCleanup) => {
  if (!value) return
  document.addEventListener('click', onOutsideClick, true)
  document.addEventListener('keydown', onKeydown)
  window.addEventListener('resize', updatePosition)
  window.addEventListener('scroll', updatePosition, true)
  onCleanup(() => {
    document.removeEventListener('click', onOutsideClick, true)
    document.removeEventListener('keydown', onKeydown)
    window.removeEventListener('resize', updatePosition)
    window.removeEventListener('scroll', updatePosition, true)
  })
})
watch(() => [props.account.id, props.account.parent_account_id, eligible.value], close)
onBeforeUnmount(cancelClose)
</script>
