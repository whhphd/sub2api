import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, type EffectScope } from 'vue'
import { useNowTicker } from '../useNowTicker'

const scopes: EffectScope[] = []
function consumer() {
  const scope = effectScope()
  scopes.push(scope)
  return { scope, now: scope.run(useNowTicker)! }
}
afterEach(() => {
  scopes.splice(0).forEach(scope => scope.stop())
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('shared account clock', () => {
  it('shares one timer, updates all rows, and releases it after the last consumer', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(1_000_000)
    const a = consumer()
    const b = consumer()
    expect(a.now).toBe(b.now)
    expect(vi.getTimerCount()).toBe(1)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(a.now.value).toBe(1_030_000)
    a.scope.stop()
    expect(vi.getTimerCount()).toBe(1)
    b.scope.stop()
    expect(vi.getTimerCount()).toBe(0)
    vi.setSystemTime(2_000_000)
    expect(consumer().now.value).toBe(2_000_000)
  })

  it('catches up immediately when the tab becomes visible', () => {
    vi.useFakeTimers()
    const a = consumer()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    vi.setSystemTime(a.now.value + 3_600_000)
    document.dispatchEvent(new Event('visibilitychange'))
    expect(a.now.value).toBe(Date.now())
  })
})
