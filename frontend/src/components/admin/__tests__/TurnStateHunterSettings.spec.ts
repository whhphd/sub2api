import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import TurnStateHunterSettings from '../TurnStateHunterSettings.vue'
const { get, proxies, save, error, success } = vi.hoisted(() => ({ get: vi.fn(), proxies: vi.fn(), save: vi.fn(), error: vi.fn(), success: vi.fn() }))
vi.mock('@/api', () => ({ adminAPI: { settings: { getOpenAIOAuthRuntimeSettings: get, updateOpenAIOAuthRuntimeSettings: save }, proxies: { getAll: proxies } } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: error, showSuccess: success }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (k: string) => k }) }))
const config = { hold_excluded_models: [] as string[], auto_models: false, rotating_proxy_ids: [] as number[], hold_when_degraded: false, usage_accounting_enabled: true, usage_api_key_id: 0, enabled: false, models: ['gpt-test'], proxy_ids: [2], max_per_hour: 300, per_account_max_per_hour: 30, gap_seconds: 20, lead_minutes: 10, retry_minutes: 10, idle_minutes: 60, reasoning_effort: 'high' }
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
  it('saves user-configured hourly limits above 600 without an input max attribute', async () => {
    await render()
    const global = wrapper.get('[data-testid="hunter-max_per_hour"]')
    const account = wrapper.get('[data-testid="hunter-per_account_max_per_hour"]')
    expect(global.attributes('max')).toBeUndefined()
    expect(account.attributes('max')).toBeUndefined()
    await global.setValue(10000)
    await account.setValue(3000)
    await wrapper.get('[data-testid="hunter-save"]').trigger('click')
    await flushPromises()
    expect(save).toHaveBeenCalledWith({ openai_oauth_turn_state_hunter: { ...config, max_per_hour: 10000, per_account_max_per_hour: 3000 } })
  })
  it.each([0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])('rejects invalid hourly limit %s', async (value) => {
    await render()
    await wrapper.get('[data-testid="hunter-max_per_hour"]').setValue(value)
    await wrapper.get('[data-testid="hunter-save"]').trigger('click')
    expect(save).not.toHaveBeenCalled()
    expect(error).toHaveBeenCalledOnce()
  })

})

it('saves global auto models, rotation and independent hold/accounting switches', async () => {
 await render()
 await wrapper.get('[data-testid="hunter-auto-models"]').setValue(true)
 expect(wrapper.get('[data-testid="hunter-models"]').attributes('disabled')).toBeDefined()
 await wrapper.get('[data-testid="hunter-rotating-2"]').setValue(true)
 await wrapper.get('[data-testid="hunter-hold-degraded"]').trigger('click')
 await wrapper.get('[data-testid="hunter-usage-accounting"]').trigger('click')
 await wrapper.get('[data-testid="hunter-save"]').trigger('click')
 await flushPromises()
 expect(save).toHaveBeenCalledWith({openai_oauth_turn_state_hunter:{...config,auto_models:true,rotating_proxy_ids:[2],hold_when_degraded:true,usage_accounting_enabled:false}})
})
it('removes rotation flags when a selected proxy is removed', async () => {
 get.mockResolvedValue({openai_oauth_turn_state_auto_enabled:true,openai_oauth_turn_state_hunter:{...config,rotating_proxy_ids:[2]}})
 await render()
 await wrapper.get('input[type="checkbox"][value="2"]').setValue(false)
 await wrapper.get('[data-testid="hunter-save"]').trigger('click');await flushPromises()
 expect(save.mock.calls[0][0].openai_oauth_turn_state_hunter.rotating_proxy_ids).toEqual([])
})
it('shows the immediate release result after saving off',async()=>{
 save.mockResolvedValue({openai_oauth_turn_state_auto_enabled:true,openai_oauth_turn_state_hunter:config,turn_state_hold_release:{released:22,complete:true}})
 await render();await wrapper.get('[data-testid="hunter-save"]').trigger('click');await flushPromises()
 expect(success).toHaveBeenCalledWith('admin.settings.turnStateHunter.released')
})

it('normalizes and saves exclusions without disabling hunting or holds',async()=>{await render();await wrapper.get('[data-testid="hunter-hold-exclusions"]').setValue(' GPT-5.6-Terra, gpt-5.6-terra ');await wrapper.get('[data-testid="hunter-save"]').trigger('click');await flushPromises();expect(save.mock.calls[0][0].openai_oauth_turn_state_hunter).toEqual({...config,hold_excluded_models:['gpt-5.6-terra']})})
