export default {
  title: '服务器带宽', settings: '带宽监控设置', global: '服务器全局', public: '公网', private: '私网 vRack',
  rx: '接收', tx: '发送', average: '区间平均', peak: '采样峰值', volume: '区间流量', capacity: '容量',
  live: '实时刷新', sampled: '采样于', monitoring: '带宽监控', enabled: '启用监控', interface: '网卡',
  rawRetention: '5 秒数据（小时）', minuteRetention: '分钟数据（天）', hourRetention: '小时数据（天）',
  noData: '暂无带宽数据', incomplete: '当前区间数据不完整，统计仅包含有效采样。',
  loadFailed: '读取带宽设置失败', saveFailed: '保存失败，请检查设置后重试。',
  addPresets: '添加告警预设（默认停用）', presetsAdded: '告警预设已添加',
  unavailable: '网络采集不可用', rxAlert: '接收带宽利用率', txAlert: '发送带宽利用率',
  states: { ok: '正常', disabled: '未启用', unconfigured: '采集服务未配置', collecting: '等待有效采样', down: '接口断开', missing: '未发现接口', error: '采集异常', stale: '数据已过期' }
}
