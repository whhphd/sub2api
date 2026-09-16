import { onMounted, onUnmounted, ref, watch, type Ref } from 'vue'
import type { OpsAccountRecentRequest, OpsAccountRecentRequestsResponse } from '@/api/admin/ops'

// One bounded batch per page/chunk, never one query per account. Only reads local logs.
export function useAccountRecentRequests(options: {
  accountIds: Readonly<Ref<number[]>>
  enabled: Readonly<Ref<boolean>>
  fetch: (ids: number[], signal: AbortSignal) => Promise<OpsAccountRecentRequestsResponse>
}) {
  const requests = ref<Record<string, OpsAccountRecentRequest[]>>({})
  const errors = ref<Record<string, boolean>>({})
  const loading = ref<Record<string, boolean>>({})
  let mounted = false
  let generation = 0
  let controller: AbortController | undefined
  let timer: ReturnType<typeof setInterval> | undefined

  const refresh = async () => {
    if (!mounted || !options.enabled.value || document.hidden || controller) return
    const ids = [...new Set(options.accountIds.value)]
    if (!ids.length) return
    const version = generation
    const request = new AbortController()
    controller = request
    loading.value = Object.fromEntries(ids.filter(id => requests.value[id] === undefined && !errors.value[id]).map(id => [id, true]))
    try {
      // Large pages are bounded to 100 IDs per request and one in-flight batch.
      for (let offset = 0; offset < ids.length; offset += 100) {
        const chunk = ids.slice(offset, offset + 100)
        try {
          const result = await options.fetch(chunk, request.signal)
          if (generation !== version || request.signal.aborted) return
          const next = { ...requests.value }
          const nextErrors = { ...errors.value }
          for (const id of chunk) {
            const items = result.accounts[String(id)]
            if (!Array.isArray(items)) { nextErrors[id] = true; continue }
            if (JSON.stringify(next[id]) !== JSON.stringify(items)) next[id] = items
            delete nextErrors[id]
          }
          requests.value = next
          errors.value = nextErrors
        } catch {
          if (generation !== version || request.signal.aborted) return
          errors.value = { ...errors.value, ...Object.fromEntries(chunk.map(id => [id, true])) }
        }
      }
    } finally {
      if (controller === request) { controller = undefined; loading.value = {} }
    }
  }

  const cancel = () => {
    generation++
    controller?.abort()
    controller = undefined
    loading.value = {}
  }
  watch(() => options.accountIds.value.join(','), () => {
    cancel()
    const ids = new Set(options.accountIds.value.map(String))
    requests.value = Object.fromEntries(Object.entries(requests.value).filter(([id]) => ids.has(id)))
    errors.value = Object.fromEntries(Object.entries(errors.value).filter(([id]) => ids.has(id)))
    void refresh()
  })
  watch(options.enabled, enabled => { if (enabled) void refresh(); else cancel() })
  const visibility = () => { if (document.hidden) cancel(); else void refresh() }
  onMounted(() => {
    mounted = true
    document.addEventListener('visibilitychange', visibility)
    timer = setInterval(() => { void refresh() }, 5000)
    void refresh()
  })
  onUnmounted(() => {
    mounted = false
    cancel()
    clearInterval(timer)
    document.removeEventListener('visibilitychange', visibility)
  })
  return { requests, errors, loading, refresh }
}
