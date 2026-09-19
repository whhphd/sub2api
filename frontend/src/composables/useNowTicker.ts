import { onScopeDispose, ref, type Ref } from 'vue'

// Adapted from KlN v0.2.7-klno.2 (f0075be5b3b9), LGPL-3.0-only.
// CallAI shares one clock across account rows and refreshes it on tab return.
export const NOW_TICKER_INTERVAL_MS = 30_000
let clock: Ref<number> | undefined
let users = 0
let timer: ReturnType<typeof setInterval> | undefined
const update = () => { if (clock) clock.value = Date.now() }
const onVisibility = () => { if (!document.hidden) update() }

export function useNowTicker(): Ref<number> {
  if (!clock) {
    clock = ref(Date.now())
    timer = setInterval(update, NOW_TICKER_INTERVAL_MS)
    document.addEventListener('visibilitychange', onVisibility)
  }
  users++
  onScopeDispose(() => {
    if (--users > 0) return
    clearInterval(timer)
    timer = undefined
    document.removeEventListener('visibilitychange', onVisibility)
    clock = undefined
  })
  return clock
}
