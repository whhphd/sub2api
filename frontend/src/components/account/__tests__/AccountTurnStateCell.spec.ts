import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import type { Account } from '@/types'
import AccountTurnStateCell from '../AccountTurnStateCell.vue'
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => `${key}${values ? ` ${Object.values(values).join('/')}` : ''}` }),
}))

const now = Date.now()
const account = (extra = {}) => ({ id: 42, platform: 'openai', type: 'oauth', extra }) as Account
const observation = { model: 'gpt-terra', length: 356, blocks: 13, shape: 'non_baseline', observed_at: new Date(now).toISOString() }
const candidate = (model = 'gpt-test') => ({ model, expires_at: new Date(now + 1000).toISOString(), failed: false })
let wrapper: VueWrapper
const trigger = () => wrapper.get('[data-testid="turn-state-trigger"]')
const dialog = () => document.querySelector('[role="dialog"]')
function render(extra = {}, enabled: boolean | null = true) {
  wrapper = mount(AccountTurnStateCell, { props: { account: account(extra), enabled, now }, attachTo: document.body })
}
afterEach(() => { wrapper?.unmount(); document.body.innerHTML = ''; vi.useRealTimers() })

describe('AccountTurnStateCell', () => {
  it('keeps the row compact and moves model/time details to a lazy hover panel', async () => {
    render({ openai_turn_state_observed: observation, openai_turn_state_summary: { candidates: [candidate('gpt-sol')] } })
    expect(dialog()).toBeNull()
    expect(wrapper.text()).toContain('TS')
    expect(wrapper.text()).toContain('356')
    expect(wrapper.text()).not.toContain('gpt-')
    expect(wrapper.text()).not.toContain('nonBaseline')
    // The observed model has no candidates: do not present another model's
    // pool as full coverage for this account.
    expect(wrapper.get('[data-testid="turn-state-coverage"]').text()).toBe('1/2')
    const bars = wrapper.findAll('[data-testid="turn-state-model-bar"]')
    expect(bars[0].classes()).toContain('bg-emerald-500')
    expect(bars[1].classes()).toContain('bg-amber-500')
    await trigger().trigger('mouseenter')
    expect(dialog()?.textContent).toContain('gpt-terra')
    expect(dialog()?.textContent).toContain('356 / 13')
    expect(dialog()?.textContent).toContain('nonBaseline')
    expect(dialog()?.textContent).toContain('gpt-sol')
    expect(dialog()?.textContent).toContain('waiting')
    expect(dialog()?.textContent).not.toContain('turnState.hint')
    expect(wrapper.text()).not.toContain('gpt-') // Teleport never grows the row.
  })

  it('updates ready, expired and rejected states as candidates age', async () => {
    const value = candidate()
    render({ openai_turn_state_summary: { candidates: [value] } })
    expect(trigger().attributes('aria-label')).toContain('ready')
    await wrapper.setProps({ now: now + 2000 })
    expect(trigger().attributes('aria-label')).toContain('expired')
    expect(wrapper.get('[data-testid="turn-state-coverage"]').text()).toBe('0/1')
    await wrapper.setProps({ account: account({ openai_turn_state_summary: { candidates: [{ ...value, failed: true }] } }) })
    expect(trigger().attributes('aria-label')).toContain('rejected')
    await wrapper.setProps({ account: account() })
    expect(trigger().attributes('aria-label')).toContain('waiting')
    expect(wrapper.get('[data-testid="turn-state-length"]').text()).toBe('--')
  })

  it('shows observation-only, unknown and parent-managed modes without implying injection', async () => {
    render({ openai_turn_state_observed: observation }, false)
    expect(trigger().attributes('aria-label')).toContain('observeOnly')
    await wrapper.setProps({ enabled: null })
    expect(trigger().attributes('aria-label')).toContain('unknown')
    await wrapper.setProps({ account: { ...account(), parent_account_id: 41 }, enabled: true })
    expect(trigger().attributes('aria-label')).toContain('inherited')
    expect(wrapper.find('[data-testid="turn-state-length"]').exists()).toBe(false)
    await trigger().trigger('click')
    expect(dialog()?.textContent).toContain('41')
    await wrapper.setProps({ account: { ...account(), type: 'apikey' } })
    expect(wrapper.find('[data-testid="turn-state-cell"]').exists()).toBe(false)
    expect(dialog()).toBeNull()
  })

  it('pins on click, supports hover transfer, and closes on Escape or outside click', async () => {
    vi.useFakeTimers()
    render()
    await trigger().trigger('mouseenter')
    await trigger().trigger('mouseleave')
    dialog()?.dispatchEvent(new MouseEvent('mouseenter'))
    await vi.advanceTimersByTimeAsync(200)
    expect(dialog()).not.toBeNull()
    await trigger().trigger('click')
    dialog()?.dispatchEvent(new MouseEvent('mouseleave'))
    await vi.advanceTimersByTimeAsync(200)
    expect(dialog()).not.toBeNull()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    expect(dialog()).toBeNull()
    await trigger().trigger('focus')
    expect(dialog()).not.toBeNull()
    await trigger().trigger('click')
    document.body.click()
    await nextTick()
    expect(dialog()).toBeNull()
    await trigger().trigger('click')
    await trigger().trigger('click')
    expect(dialog()).toBeNull()
  })

  it('caps the inline graphic, keeps every model in details and closes when a row is reused', async () => {
    render({ openai_turn_state_summary: { candidates: Array.from({ length: 16 }, (_, i) => candidate(`model-${i}`)) } })
    expect(wrapper.findAll('[data-testid="turn-state-model-bar"]')).toHaveLength(6)
    expect(wrapper.text()).toContain('+10')
    await trigger().trigger('click')
    expect(dialog()?.textContent).toContain('model-15')
    await wrapper.setProps({ account: { ...account(), id: 99 } })
    expect(dialog()).toBeNull()
  })
})
