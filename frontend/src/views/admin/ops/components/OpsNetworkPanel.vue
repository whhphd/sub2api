<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, Legend, LineElement, LinearScale, PointElement, Tooltip, type ChartOptions } from 'chart.js'
import { Line } from 'vue-chartjs'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import { opsNetworkAPI, type NetworkOverview, type NetworkRange, type NetworkTrend } from '@/api/admin/opsNetwork'
import { networkBytes, networkNumber, networkPercent } from '../utils/networkFormatters'
import OpsNetworkSettingsDialog from './OpsNetworkSettingsDialog.vue'

ChartJS.register(CategoryScale, Legend, LineElement, LinearScale, PointElement, Tooltip)
const props = defineProps<{ timeRange: string; customStartTime?: string | null; customEndTime?: string | null; fullscreen?: boolean }>()
const { t } = useI18n()
const overview = ref<NetworkOverview | null>(null)
const trend = ref<NetworkTrend | null>(null)
const selectedLink = ref('public')
const live = ref(true)
const loading = ref(true)
const failed = ref(false)
const settingsOpen = ref(false)
const dark = ref(false)
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined
let observer: MutationObserver | undefined
let controller: AbortController | undefined
let sequence = 0
let lastTrend = 0
const links = computed(() => overview.value?.links.filter(link => link.enabled) ?? [])
const current = computed(() => overview.value?.links.find(link => link.id === selectedLink.value))
const sampledAt = computed(() => {
  const value = Date.parse(overview.value?.collected_at ?? '')
  return Number.isFinite(value) && value > 0 ? value : null
})
const status = computed(() => {
  if (failed.value) return 'error'
  const state = current.value?.status ?? overview.value?.status ?? 'collecting'
  if (!['disabled', 'unconfigured'].includes(state) && sampledAt.value && now.value - sampledAt.value > 15000) return 'stale'
  return state
})
const directions = ['rx', 'tx'] as const
const range = (): NetworkRange => props.timeRange === 'custom'
  ? { start_time: props.customStartTime ?? undefined, end_time: props.customEndTime ?? undefined, link: selectedLink.value }
  : { time_range: props.timeRange, link: selectedLink.value }

async function refresh(forceTrend = false) {
  if (document.hidden || controller) return
  const request = new AbortController()
  controller = request
  const id = ++sequence
  try {
    const result = await opsNetworkAPI.overview(request.signal)
    if (id !== sequence) return
    overview.value = result
    if (!result.links.some(link => link.id === selectedLink.value && link.enabled)) selectedLink.value = 'public'
    if (forceTrend || Date.now() - lastTrend >= (props.timeRange === 'custom' ? 60000 : 5000)) {
      const history = await opsNetworkAPI.trend(range(), request.signal)
      if (id !== sequence) return
      trend.value = history
      lastTrend = Date.now()
    }
    failed.value = false
  } catch {
    if (!request.signal.aborted && id === sequence) failed.value = true
  } finally {
    if (id === sequence) { controller = undefined; loading.value = false }
  }
}
function cancelRequest() { sequence++; controller?.abort(); controller = undefined }
function visibilityChanged() {
  if (document.hidden) cancelRequest()
  else if (live.value) void refresh(true)
}
function reloadRange() { cancelRequest(); trend.value = null; void refresh(true) }
watch(() => [props.timeRange, props.customStartTime, props.customEndTime, selectedLink.value], reloadRange)
watch(live, value => { if (value) void refresh(true); else cancelRequest() })
onMounted(() => {
  dark.value = document.documentElement.classList.contains('dark')
  observer = new MutationObserver(() => { dark.value = document.documentElement.classList.contains('dark') })
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
  document.addEventListener('visibilitychange', visibilityChanged)
  void refresh(true)
  timer = setInterval(() => { now.value = Date.now(); if (live.value) void refresh() }, 5000)
})
onUnmounted(() => { clearInterval(timer); observer?.disconnect(); document.removeEventListener('visibilitychange', visibilityChanged); cancelRequest() })

const chartData = computed(() => {
  const points = trend.value?.points ?? []
  const datasets = directions.flatMap(direction => {
    const color = direction === 'rx' ? '#0891b2' : '#16a34a'
    const base = { borderColor: color, pointRadius: 0, pointHitRadius: 10, borderWidth: 2, spanGaps: false, tension: 0 }
    const series = [{ ...base, label: t(`admin.ops.network.${direction}`), data: points.map(p => p[`${direction}_avg_mbps`]) }]
    if ((trend.value?.bucket_seconds ?? 5) > 5) series.push({ ...base, label: `${t(`admin.ops.network.${direction}`)} ${t('admin.ops.network.peak')}`, data: points.map(p => p[`${direction}_peak_mbps`]), borderWidth: 1 })
    return series
  })
  for (const direction of directions) {
    datasets.push({ label: `${t(`admin.ops.network.${direction}`)} ${t('admin.ops.network.capacity')}`, data: points.map(p => p[`${direction}_capacity_mbps`] || null), borderColor: dark.value ? '#64748b' : '#94a3b8', borderWidth: 1, pointRadius: 0, pointHitRadius: 0, spanGaps: false, tension: 0 })
  }
  return { labels: points.map(p => new Date(p.bucket_start).toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', ...(trend.value?.bucket_seconds === 5 ? { second: '2-digit' as const } : {}) })), datasets }
})
const chartOptions = computed<ChartOptions<'line'>>(() => ({
  responsive: true, maintainAspectRatio: false, animation: false,
  interaction: { mode: 'index', intersect: false },
  plugins: { legend: { position: 'bottom', labels: { color: dark.value ? '#cbd5e1' : '#475569', boxWidth: 16 } } },
  scales: {
    x: { ticks: { maxTicksLimit: 6, maxRotation: 0, color: dark.value ? '#94a3b8' : '#64748b' }, grid: { display: false } },
    y: { beginAtZero: true, suggestedMax: Math.max(current.value?.rx_capacity_mbps ?? 0, current.value?.tx_capacity_mbps ?? 0), title: { display: true, text: 'Mbps' }, ticks: { color: dark.value ? '#94a3b8' : '#64748b' }, grid: { color: dark.value ? '#334155' : '#e2e8f0' } }
  }
}))
function rate(direction: 'rx' | 'tx') { return status.value === 'ok' ? current.value?.[`${direction}_mbps`] : null }
function percent(direction: 'rx' | 'tx') { return networkPercent(rate(direction), current.value?.[`${direction}_capacity_mbps`] ?? 0) }
function meterColor(direction: 'rx' | 'tx') {
  const value = percent(direction) ?? 0
  return value >= 90 ? 'bg-red-500' : value >= 80 ? 'bg-amber-500' : direction === 'rx' ? 'bg-cyan-600' : 'bg-green-600'
}
</script>

<template>
  <section class="border-y border-gray-200 bg-white px-4 py-5 dark:border-dark-700 dark:bg-dark-900 md:px-6" data-testid="network-panel">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex min-w-0 flex-wrap items-center gap-2">
        <Icon name="globe" size="md" class="text-cyan-600" />
        <h2 class="text-base font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.ops.network.title') }}</h2>
        <span class="text-xs text-gray-500">{{ overview?.server_id || '--' }} · {{ t('admin.ops.network.global') }}</span>
      </div>
      <div class="flex items-center gap-3">
        <label class="flex items-center gap-2 text-xs text-gray-500">{{ t('admin.ops.network.live') }}<Toggle v-model="live" :aria-label="t('admin.ops.network.live')" /></label>
        <button type="button" class="flex h-8 w-8 items-center justify-center rounded-md hover:bg-gray-100 dark:hover:bg-dark-700" :title="t('common.refresh')" :aria-label="t('common.refresh')" @click="refresh(true)"><Icon name="refresh" size="sm" /></button>
        <button v-if="!fullscreen" type="button" class="flex h-8 w-8 items-center justify-center rounded-md hover:bg-gray-100 dark:hover:bg-dark-700" :title="t('admin.ops.network.settings')" :aria-label="t('admin.ops.network.settings')" @click="settingsOpen = true"><Icon name="cog" size="sm" /></button>
      </div>
    </div>
    <div class="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs">
      <div class="flex items-center gap-2">
        <template v-if="links.length > 1"><button v-for="link in links" :key="link.id" type="button" class="border-b-2 px-2 py-1" :class="selectedLink === link.id ? 'border-cyan-600 text-cyan-700' : 'border-transparent text-gray-500'" @click="selectedLink = link.id">{{ t(`admin.ops.network.${link.id}`) }}</button></template>
        <span v-else class="text-gray-500">{{ t('admin.ops.network.public') }}</span>
        <span class="font-mono text-gray-500">{{ current?.device }}</span>
        <span :class="status === 'ok' ? 'text-green-600' : 'text-amber-600'">{{ t(`admin.ops.network.states.${status}`) }}</span>
      </div>
      <span class="text-gray-500">{{ t('admin.ops.network.sampled') }} {{ sampledAt ? new Date(sampledAt).toLocaleTimeString() : '--' }}</span>
    </div>
    <div class="mt-4 grid grid-cols-1 gap-5 md:grid-cols-2">
      <div v-for="direction in directions" :key="direction" class="min-w-0">
        <div class="flex items-center gap-1 text-sm text-gray-500"><Icon :name="direction === 'rx' ? 'arrowDown' : 'arrowUp'" size="sm" />{{ t(`admin.ops.network.${direction}`) }}</div>
        <div class="mt-1 flex flex-wrap items-baseline gap-2"><span class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-gray-100">{{ networkNumber(rate(direction)) }}</span><span class="text-xs text-gray-500">Mbps / {{ networkNumber(current?.[`${direction}_capacity_mbps`]) }} Mbps</span><span class="ml-auto text-sm font-medium tabular-nums">{{ networkNumber(percent(direction)) }}%</span></div>
        <div class="mt-2 h-1.5 overflow-hidden rounded-sm bg-gray-100 dark:bg-dark-700"><div class="h-full" :class="meterColor(direction)" :style="{ width: `${Math.min(100, Math.max(0, percent(direction) ?? 0))}%` }" /></div>
        <dl class="mt-3 grid grid-cols-3 gap-2 text-xs">
          <div><dt class="text-gray-500">{{ t('admin.ops.network.average') }}</dt><dd class="mt-1 tabular-nums">{{ networkNumber(trend?.summary[`${direction}_avg_mbps`]) }} Mbps</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.ops.network.peak') }}</dt><dd class="mt-1 tabular-nums">{{ networkNumber(trend?.summary[`${direction}_peak_mbps`]) }} Mbps</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.ops.network.volume') }}</dt><dd class="mt-1 tabular-nums">{{ trend?.summary.valid_seconds ? networkBytes(trend.summary[`${direction}_bytes`]) : '--' }}</dd></div>
        </dl>
      </div>
    </div>
    <div class="relative mt-5 h-[260px] min-w-0 md:h-[290px]">
      <Line v-if="trend?.points.some(p => p.valid_seconds > 0)" :data="chartData" :options="chartOptions" />
      <div v-else class="flex h-full items-center justify-center text-sm text-gray-500">{{ loading ? t('common.loading') : t('admin.ops.network.noData') }}</div>
    </div>
    <div v-if="trend && !trend.summary.complete" class="mt-1 text-xs text-amber-600">{{ t('admin.ops.network.incomplete') }}</div>
    <OpsNetworkSettingsDialog v-if="!fullscreen" :show="settingsOpen" @close="settingsOpen = false" @saved="reloadRange" />
  </section>
</template>
