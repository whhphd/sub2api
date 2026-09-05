import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import OpsNetworkPanel from '../OpsNetworkPanel.vue'
import { networkBytes, networkNumber, networkPercent } from '../../utils/networkFormatters'

const api = vi.hoisted(() => ({ overview: vi.fn(), trend: vi.fn() }))
vi.mock('@/api/admin/opsNetwork', () => ({ opsNetworkAPI: api }))
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-chartjs', async () => ({ Line: (await import('vue')).defineComponent({ name: 'NetworkChart', props: ['data', 'options'], template: '<div class="chart-stub" />' }) }))
const SettingsStub = defineComponent({ props: ['show'], template: '<div />' })
const makeOverview = () => ({ server_id: 'test-host', collected_at: new Date().toISOString(), status: 'ok', devices: [], links: [
  { id: 'public', device: 'enp6s0', enabled: true, status: 'ok', rx_mbps: 800, tx_mbps: 400, rx_capacity_mbps: 1000, tx_capacity_mbps: 1000 },
  { id: 'private', device: 'enp7s0', enabled: false, status: 'disabled', rx_mbps: null, tx_mbps: null, rx_capacity_mbps: 1000, tx_capacity_mbps: 1000 }
] })
const history = { bucket_seconds: 5, summary: { rx_avg_mbps: 600, tx_avg_mbps: 300, rx_peak_mbps: 850, tx_peak_mbps: 450, rx_bytes: 1e9, tx_bytes: 5e8, valid_seconds: 10, complete: false }, points: [
  { bucket_start: '2026-09-05T12:00:00Z', rx_avg_mbps: 800, tx_avg_mbps: 400, valid_seconds: 5 },
  { bucket_start: '2026-09-05T12:00:05Z', rx_avg_mbps: null, tx_avg_mbps: null, valid_seconds: 0 }
] }
function render() { return mount(OpsNetworkPanel, { props: { timeRange: '1h' }, global: { stubs: { OpsNetworkSettingsDialog: SettingsStub } } }) }

describe('OpsNetworkPanel', () => {
  beforeEach(() => {
    vi.useFakeTimers(); vi.clearAllMocks()
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    api.overview.mockImplementation(async () => makeOverview())
    api.trend.mockResolvedValue(history)
  })
  afterEach(() => { vi.useRealTimers() })

  it('shows independent directional utilization and preserves chart gaps', async () => {
    const wrapper = render(); await flushPromises()
    expect(wrapper.text()).toContain('80%'); expect(wrapper.text()).toContain('40%')
    expect(wrapper.text()).not.toContain('enp7s0')
    expect(api.trend).toHaveBeenCalledWith({ time_range: '1h', link: 'public' }, expect.any(AbortSignal))
    expect(wrapper.findComponent({ name: 'NetworkChart' }).props('data').datasets[0].data).toEqual([800, null])
    expect(wrapper.text()).toContain('admin.ops.network.incomplete')
    wrapper.unmount()
  })

  it('refreshes independently and pauses while hidden or switched off', async () => {
    const wrapper = render(); await flushPromises()
    await vi.advanceTimersByTimeAsync(5000); await flushPromises(); expect(api.overview).toHaveBeenCalledTimes(2)
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(10000); expect(api.overview).toHaveBeenCalledTimes(2)
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    document.dispatchEvent(new Event('visibilitychange')); await flushPromises(); expect(api.overview).toHaveBeenCalledTimes(3)
    await wrapper.find('[role="switch"]').trigger('click')
    await vi.advanceTimersByTimeAsync(10000); expect(api.overview).toHaveBeenCalledTimes(3)
    wrapper.unmount(); await vi.advanceTimersByTimeAsync(10000); expect(api.overview).toHaveBeenCalledTimes(3)
  })

  it('displays missing values on fetch failures rather than retaining a healthy reading', async () => {
    const wrapper = render(); await flushPromises()
    api.overview.mockRejectedValue(new Error('offline'))
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.network.states.error')
    expect(wrapper.text()).not.toContain('80%'); expect(wrapper.text()).toContain('--')
    wrapper.unmount()
  })

  it('changes history range without platform or group filters', async () => {
    const wrapper = render(); await flushPromises()
    await wrapper.setProps({ timeRange: 'custom', customStartTime: '2026-09-01T00:00:00Z', customEndTime: '2026-09-02T00:00:00Z' }); await flushPromises()
    expect(api.trend).toHaveBeenLastCalledWith({ start_time: '2026-09-01T00:00:00Z', end_time: '2026-09-02T00:00:00Z', link: 'public' }, expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('uses decimal bandwidth and traffic units', () => {
    expect(networkPercent(500, 1000)).toBe(50)
    expect(networkBytes(1e9)).toBe('1 GB'); expect(networkBytes(1e12)).toBe('1 TB')
    expect(networkNumber(null)).toBe('--'); expect(networkPercent(null, 1000)).toBeNull()
  })
})
