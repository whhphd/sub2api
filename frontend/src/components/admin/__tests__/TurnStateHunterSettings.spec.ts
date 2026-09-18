import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import TurnStateHunterSettings from '../TurnStateHunterSettings.vue'
const { get, proxies, save, error, success } = vi.hoisted(() => ({ get: vi.fn(), proxies: vi.fn(), save: vi.fn(), error: vi.fn(), success: vi.fn() }))
vi.mock('@/api', () => ({ adminAPI: { settings: { getOpenAIOAuthRuntimeSettings: get, updateOpenAIOAuthRuntimeSettings: save }, proxies: { getAll: proxies } } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: error, showSuccess: success }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (k: string) => k }) }))
const config = { enabled: false, models: ['gpt-test'], proxy_ids: [2], max_per_hour: 300, per_account_max_per_hour: 30, gap_seconds: 20, lead_minutes: 10, retry_minutes: 10, idle_minutes: 60, reasoning_effort: 'high' }
let wrapper: VueWrapper
beforeEach(() => {
  vi.clearAllMocks()
  get.mockResolvedValue({ openai_oauth_turn_state_auto_enabled: true, openai_oauth_turn_state_hunter: structuredClone(config) })
  proxies.mockResolvedValue([{ id: 2, name: 'US rotate', host: 'us.1024proxy.io', port: 3000, status: 'active', username: 'private-user', password: 'private-password' }])
  save.mockImplementation(async (p) => ({ ...p, openai_oauth_turn_state_auto_enabled: true }))
})
afterEach(() => wrapper?.unmount())
async function render() { wrapper = mount(TurnStateHunterSettings); await flushPromises() }
describe('global turn-state hunter settings', () => {
  it('loads global policy and named proxies, then saves only the hunter object', async () => {
    await render()
    expect(wrapper.text()).toContain('US rotate')
    expect(wrapper.text()).not.toContain('private-')
    await wrapper.get('[data-testid="hunter-models"]').setValue('gpt-a, gpt-b')
    await wrapper.get('[data-testid="hunter-save"]').trigger('click')
    await flushPromises()
    expect(save).toHaveBeenCalledWith({ openai_oauth_turn_state_hunter: { ...config, models: ['gpt-a', 'gpt-b'] } })
    expect(success).toHaveBeenCalledOnce()
  })
  it('rejects oversized lists without silently truncating', async () => {
    await render()
    await wrapper.get('[data-testid="hunter-models"]').setValue(Array.from({ length: 9 }, (_, i) => `model-${i}`).join(','))
    await wrapper.get('[data-testid="hunter-save"]').trigger('click')
    expect(save).not.toHaveBeenCalled()
    expect(error).toHaveBeenCalledOnce()
  })
  it('does not save fallback defaults after a failed load', async () => {
    get.mockRejectedValue(new Error('offline'))
    await render()
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.get('fieldset').attributes('disabled')).toBeDefined()
    expect(save).not.toHaveBeenCalled()
  })
  it('shows the takeover prerequisite and surfaces rejected saves', async () => {
    get.mockResolvedValue({ openai_oauth_turn_state_auto_enabled: false, openai_oauth_turn_state_hunter: structuredClone(config) })
    await render()
    expect(wrapper.text()).toContain('needsAuto')
    save.mockRejectedValue(new Error('save failed'))
    await wrapper.get('[data-testid="hunter-save"]').trigger('click')
    await flushPromises()
    expect(error).toHaveBeenCalledOnce()
    expect(success).not.toHaveBeenCalled()
  })
})
