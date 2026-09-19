<template>
  <span v-if="models.length" class="inline-flex shrink-0">
    <button ref="trigger" type="button" class="badge badge-warning inline-flex h-6 items-center gap-1 whitespace-nowrap text-xs focus-visible:outline focus-visible:outline-2 focus-visible:outline-amber-500" :aria-label="label" :aria-expanded="open" aria-haspopup="dialog" data-testid="model-hold-badge" @mouseenter="show" @mouseleave="scheduleClose" @focus="show" @blur="scheduleClose" @click.stop="togglePinned">
      <span aria-hidden="true" class="font-bold">Ⅱ</span><span>{{ t('admin.accounts.status.holdCompact') }}</span><span class="font-mono tabular-nums">{{ models.length }}</span>
    </button>
    <Teleport to="body">
      <div v-if="open" ref="panel" role="dialog" :aria-label="label" :style="panelStyle" class="fixed z-[99999] overflow-y-auto rounded-lg bg-gray-900 p-3 text-xs text-white shadow-xl ring-1 ring-white/10 dark:bg-gray-800" @mouseenter="cancelClose" @mouseleave="scheduleClose" @focusin="cancelClose" @focusout="scheduleClose">
        <div class="mb-2 flex items-center gap-2"><span class="font-medium">{{ t('admin.accounts.status.turnStateHold') }}</span><button type="button" :aria-label="t('common.close')" class="ml-auto rounded px-1 hover:bg-white/10" @click.stop="close">×</button></div>
        <ul class="space-y-1"><li v-for="model in models" :key="model" class="break-all">{{ model }}</li></ul>
        <p class="mt-2 border-t border-white/10 pt-2 text-gray-300">{{ t('admin.accounts.status.holdDetail') }}</p>
      </div>
    </Teleport>
  </span>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch, type CSSProperties } from 'vue'
import { useI18n } from 'vue-i18n'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'
const props = defineProps<{ models: string[]; accountId: number }>()
const { t } = useI18n()
const label = computed(() => `${t('admin.accounts.status.turnStateHold')}: ${props.models.join(', ')}`)
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
  const p = getFloatingPanelPosition(trigger.value.getBoundingClientRect(), window.innerWidth, window.innerHeight, { maxWidth: 280 })
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
watch(() => [props.accountId, props.models.join("\x00")], close)
onBeforeUnmount(cancelClose)
</script>
