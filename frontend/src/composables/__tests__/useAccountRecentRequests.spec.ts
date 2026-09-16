import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { useAccountRecentRequests } from '../useAccountRecentRequests'
import type { OpsAccountRecentRequestsResponse } from '@/api/admin/ops'

let wrapper: VueWrapper
const response = (ids: number[]): OpsAccountRecentRequestsResponse => ({
  accounts: Object.fromEntries(ids.map(id => [id, [{ id, kind: 'success', account_id: id, request_id: `r-${id}`, created_at: '2026-09-16T01:00:00Z' }]])),
  sampled_at: '2026-09-16T01:00:00Z', window_hours: 24, limit: 10
})
beforeEach(() => { vi.useFakeTimers(); Object.defineProperty(document, 'hidden', { configurable: true, value: false }) })
afterEach(() => { wrapper?.unmount(); vi.useRealTimers() })
function setup(ids = [1, 2]) {
  const accountIds = ref(ids), enabled = ref(true)
  const fetch = vi.fn(async (ids: number[], _signal: AbortSignal) => response(ids))
  let state!: ReturnType<typeof useAccountRecentRequests>
  wrapper = mount(defineComponent({ setup() { state = useAccountRecentRequests({ accountIds, enabled, fetch }); return () => null } }))
  return { accountIds, enabled, fetch, get state() { return state } }
}
describe('account recent request polling', () => {
  it('fetches the page in one batch, pauses when hidden/disabled and immediately resumes', async () => {
    const x = setup(); await flushPromises()
    expect(x.fetch).toHaveBeenCalledTimes(1); expect(x.fetch.mock.calls[0][0]).toEqual([1,2])
    await vi.advanceTimersByTimeAsync(5000); expect(x.fetch).toHaveBeenCalledTimes(2)
    Object.defineProperty(document, 'hidden', { configurable: true, value: true }); document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(10000); expect(x.fetch).toHaveBeenCalledTimes(2)
    Object.defineProperty(document, 'hidden', { configurable: true, value: false }); document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises(); expect(x.fetch).toHaveBeenCalledTimes(3)
    x.enabled.value = false; await flushPromises(); await vi.advanceTimersByTimeAsync(5000)
    expect(x.fetch).toHaveBeenCalledTimes(3)
    x.enabled.value = true; await flushPromises(); expect(x.fetch).toHaveBeenCalledTimes(4)
    wrapper.unmount(); await vi.advanceTimersByTimeAsync(5000); expect(x.fetch).toHaveBeenCalledTimes(4)
  })
  it('does not overlap slow refreshes and ignores stale page responses even when cancellation is ignored', async () => {
    const x = setup(); await flushPromises()
    let resolve!: (value: OpsAccountRecentRequestsResponse) => void
    x.fetch.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    await vi.advanceTimersByTimeAsync(5000)
    await vi.advanceTimersByTimeAsync(5000); expect(x.fetch).toHaveBeenCalledTimes(2)
    x.accountIds.value = [3]; await flushPromises()
    expect(x.fetch.mock.calls[1][1].aborted).toBe(true)
    resolve(response([1,2])); await flushPromises()
    expect(Object.keys(x.state.requests.value)).toEqual(['3'])
  })
  it('preserves successful values on failure, marks them stale, and clears error on recovery', async () => {
    const x = setup(); await flushPromises(); const before = x.state.requests.value['1']
    x.fetch.mockRejectedValueOnce(new Error('offline'))
    await vi.advanceTimersByTimeAsync(5000)
    expect(x.state.requests.value['1']).toBe(before); expect(x.state.errors.value['1']).toBe(true)
    await vi.advanceTimersByTimeAsync(5000); expect(x.state.errors.value['1']).toBeUndefined()
  })
  it('bounds large pages and keeps partial successes', async () => {
    const x = setup(Array.from({length: 101}, (_,i) => i+1)); await flushPromises()
    expect(x.fetch.mock.calls.map(c => c[0].length)).toEqual([100,1])
    expect(Object.keys(x.state.requests.value)).toHaveLength(101)
    x.fetch.mockResolvedValueOnce(response(Array.from({length:100},(_,i)=>i+1))).mockRejectedValueOnce(new Error('offline'))
    await vi.advanceTimersByTimeAsync(5000)
    expect(x.state.errors.value['101']).toBe(true); expect(x.state.errors.value['1']).toBeUndefined()
  })
})
