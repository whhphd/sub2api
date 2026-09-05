import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import UpstreamBalanceCell from '../UpstreamBalanceCell.vue'
import type { Account, UpstreamBalanceSnapshot } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('en') }) }))
const now = Date.parse('2026-09-05T12:00:00Z')
const snapshot: UpstreamBalanceSnapshot = { status: 'ok', scope: 'wallet', balance: 0, currency: 'USD', last_attempt_at: '2026-09-05T11:59:00Z', next_query_at: '2026-09-05T12:29:00Z', fresh_until: '2026-09-05T12:59:00Z' }
function render(data?: UpstreamBalanceSnapshot, type = 'apikey') {
  return mount(UpstreamBalanceCell, {
    props: { account: { id: 1, type, extra: { upstream_balance: data } } as Account, now },
    global: { stubs: { HelpTooltip: { template: '<div><slot name="trigger" /><slot /></div>' }, Icon: true } }
  })
}

describe('UpstreamBalanceCell', () => {
  it('shows zero as a valid balance and queries only on explicit click', async () => {
    const wrapper = render(snapshot)
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).toBe('0 USD')
    expect(wrapper.emitted('query')).toBeUndefined()
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('query')).toHaveLength(1)
  })
  it('keeps the previous balance on query failure and displays stale status', () => {
    const wrapper = render({ ...snapshot, status: 'failed', balance: 15.5, fresh_until: '2026-09-05T11:00:00Z' })
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).toBe('15.5 USD')
    expect(wrapper.text()).toContain('status.stale')
  })
  it('distinguishes unconfirmed quota and missing wallet data', () => {
    expect(render({ ...snapshot, status: 'unconfirmed', scope: 'unknown', balance: 20 }).get('[data-testid="upstream-balance-value"]').text()).toBe('20 USD *')
    expect(render({ ...snapshot, status: 'non_wallet', balance: undefined }).text()).toContain('status.non_wallet')
    expect(render().get('[data-testid="upstream-balance-value"]').text()).toBe('--')
  })
  it('has no query action for OAuth accounts and prevents repeated clicks while loading', async () => {
    expect(render(undefined, 'oauth').find('button').exists()).toBe(false)
    const wrapper = render(snapshot)
    await wrapper.setProps({ querying: true })
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
  })
})
