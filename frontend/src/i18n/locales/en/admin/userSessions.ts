export default {
  userSessions: {
    title: 'User Sessions',
    description: 'Standalone per-user input archives. This feature is independent from Prompt Audit and Risk Control.',
    filterUser: 'User ID',
    filterUserPlaceholder: 'All users',
    columns: { id: 'Session', user: 'User', protocol: 'Protocol / Model', counts: 'Continuity', updated: 'Updated', actions: 'Actions' },
    turns: '{turns} turns · {requests} requests',
    view: 'View', export: 'Export ZIP', download: 'Download', delete: 'Delete',
    detailTitle: 'Session #{id}',
    partMeta: '{kind} · {size} bytes · {mime}',
    empty: 'No saved user sessions',
    deleteTitle: 'Delete saved session',
    deleteMessage: 'Delete session #{id}? Content blobs no longer referenced by this user will also be removed.',
    deleted: 'Session deleted',
    errors: { list: 'Failed to load sessions', detail: 'Failed to load session', download: 'Download failed', export: 'Export failed', delete: 'Delete failed' }
  }
}
