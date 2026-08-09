export default {
  userSessions: {
    title: '用户会话',
    description: '按用户独立保存输入内容；此功能不属于提示词审计或风控。',
    filterUser: '用户 ID',
    filterUserPlaceholder: '全部用户',
    columns: { id: '会话', user: '用户', protocol: '协议 / 模型', counts: '连续性', updated: '更新时间', actions: '操作' },
    turns: '{turns} 轮 · {requests} 次请求',
    view: '查看', export: '导出 ZIP', download: '下载', delete: '删除',
    detailTitle: '会话 #{id}',
    partMeta: '{kind} · {size} 字节 · {mime}',
    empty: '暂无保存的用户会话',
    deleteTitle: '删除用户会话',
    deleteMessage: '确定删除会话 #{id} 吗？该用户不再引用的内容对象也会一并删除。',
    deleted: '会话已删除',
    errors: { list: '加载会话失败', detail: '加载会话详情失败', download: '下载失败', export: '导出失败', delete: '删除失败' }
  }
}
