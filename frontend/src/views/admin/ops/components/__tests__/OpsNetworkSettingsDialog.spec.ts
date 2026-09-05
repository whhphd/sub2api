import { describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import OpsNetworkSettingsDialog from '../OpsNetworkSettingsDialog.vue'

const api = vi.hoisted(() => ({ settings: vi.fn(), updateSettings: vi.fn(), listAlertRules: vi.fn(), createAlertRule: vi.fn() }))
vi.mock('@/api/admin/opsNetwork', () => ({ opsNetworkAPI: api }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: api }))
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))

const fixture = () => ({ source_configured: true, server_id: 'host', devices: [{ name: 'enp6s0' }], alert_presets: [
  { name: 'RX warning', metric_type: 'network_rx_utilization_percent', threshold: 80, filters: { network_link: 'public' } }
], settings: { enabled: true, raw_retention_hours: 6, minute_retention_days: 30, hourly_retention_days: 180, links: [
  { id: 'public', enabled: true, device: 'enp6s0', rx_capacity_mbps: 1000, tx_capacity_mbps: 1000 },
  { id: 'private', enabled: false, device: 'enp7s0', rx_capacity_mbps: 1000, tx_capacity_mbps: 1000 }
] } })

describe('OpsNetworkSettingsDialog', () => {
  it('saves changed directional capacity while retaining disabled private networking', async () => {
    api.settings.mockResolvedValue(fixture()); api.updateSettings.mockResolvedValue(fixture())
    const wrapper = mount(OpsNetworkSettingsDialog, { props: { show: false }, global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' } } } })
    await wrapper.setProps({ show: true }); await flushPromises()
    await wrapper.get('[aria-label="public rx Mbps"]').setValue('2000')
    const save = wrapper.findAll('button').find(b => b.text() === 'common.save')!
    await save.trigger('click'); await flushPromises()
    expect(api.updateSettings).toHaveBeenCalledWith(expect.objectContaining({ links: [expect.objectContaining({ rx_capacity_mbps: 2000, tx_capacity_mbps: 1000 }), expect.objectContaining({ enabled: false })] }))
    expect(wrapper.emitted('saved')).toHaveLength(1)
    wrapper.unmount()
  })

  it('adds disabled alert presets and skips equivalent existing rules', async () => {
    api.settings.mockResolvedValue(fixture()); api.listAlertRules.mockResolvedValue([]); api.createAlertRule.mockResolvedValue({ id: 1 })
    const wrapper = mount(OpsNetworkSettingsDialog, { props: { show: false }, global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' } } } })
    await wrapper.setProps({ show: true }); await flushPromises()
    await wrapper.findAll('button').find(b => b.text().includes('admin.ops.network.addPresets'))!.trigger('click'); await flushPromises()
    expect(api.createAlertRule).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, notify_email: false }))
    api.createAlertRule.mockClear(); api.listAlertRules.mockResolvedValue(fixture().alert_presets)
    await wrapper.setProps({ show: false }); await wrapper.setProps({ show: true }); await flushPromises()
    await wrapper.findAll('button').find(b => b.text().includes('admin.ops.network.addPresets'))!.trigger('click'); await flushPromises()
    expect(api.createAlertRule).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
