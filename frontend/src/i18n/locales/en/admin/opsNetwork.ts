export default {
  title: 'Server Bandwidth', settings: 'Bandwidth Settings', global: 'Whole server', public: 'Public', private: 'Private vRack',
  rx: 'Receive', tx: 'Transmit', average: 'Period average', peak: 'Sample peak', volume: 'Period traffic', capacity: 'Capacity',
  live: 'Live refresh', sampled: 'Sampled at', monitoring: 'Bandwidth monitoring', enabled: 'Monitoring enabled', interface: 'Interface',
  rawRetention: '5-second data (hours)', minuteRetention: 'Minute data (days)', hourRetention: 'Hourly data (days)',
  noData: 'No bandwidth data', incomplete: 'Incomplete period. Statistics include valid samples only.',
  loadFailed: 'Failed to load bandwidth settings', saveFailed: 'Save failed. Check the settings and try again.',
  addPresets: 'Add alert presets (disabled)', presetsAdded: 'Alert presets added',
  unavailable: 'Network collection unavailable', rxAlert: 'Receive bandwidth utilization', txAlert: 'Transmit bandwidth utilization',
  states: { ok: 'Healthy', disabled: 'Disabled', unconfigured: 'Collector not configured', collecting: 'Awaiting samples', down: 'Link down', missing: 'Interface missing', error: 'Collection error', stale: 'Stale data' }
}
