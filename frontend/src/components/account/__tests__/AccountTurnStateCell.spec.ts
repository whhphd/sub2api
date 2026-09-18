import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import type { Account } from '@/types'
import AccountTurnStateCell from '../AccountTurnStateCell.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const now = Date.now()
const account = (extra = {}) => ({ id: 42, platform: 'openai', type: 'oauth', extra }) as Account
describe('AccountTurnStateCell', () => {
  it('shows observation only when global takeover is off', () => {
    const wrapper = mount(AccountTurnStateCell, { props: { account: account({ openai_turn_state_observed: { model: 'gpt-test', length: 356, blocks: 13, shape: 'non_baseline', observed_at: new Date(now).toISOString() } }), enabled: false, now } })
    expect(wrapper.text()).toContain('observeOnly')
    expect(wrapper.text()).toContain('356 / 13')
    expect(wrapper.text()).toContain('nonBaseline')
  })
  it('updates ready, expired and rejected states without receiving a token', async () => {
    const candidate = { model: 'gpt-test', expires_at: new Date(now + 1000).toISOString(), failed: false }
    const wrapper = mount(AccountTurnStateCell, { props: { account: account({ openai_turn_state_summary: { candidates: [candidate] } }), enabled: true, now } })
    expect(wrapper.text()).toContain('ready')
    await wrapper.setProps({ now: now + 2000 })
    expect(wrapper.text()).toContain('expired')
    await wrapper.setProps({ account: account({ openai_turn_state_summary: { candidates: [{ ...candidate, failed: true }] } }) })
    expect(wrapper.text()).toContain('rejected')
    await wrapper.setProps({ account: account() })
    expect(wrapper.text()).toContain('waiting')
  })
  it('shows inherited scope and hides non-OAuth accounts', async () => {
    const wrapper = mount(AccountTurnStateCell, { props: { account: { ...account(), parent_account_id: 41 }, enabled: true, now } })
    expect(wrapper.text()).toContain('inherited')
    await wrapper.setProps({ account: { ...account(), type: 'apikey' } })
    expect(wrapper.find('[data-testid="turn-state-cell"]').exists()).toBe(false)
  })
})
