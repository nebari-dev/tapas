import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp, type Call } from '../../../test/api'
import { adminMe, me } from '../../../test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

const REDIRECT = 'https://tapas.example.org/settings/connections/callback'

const chat = {
  id: 'c-1',
  rev: 'r-1',
  catalogId: 'chat',
  slug: 'chat',
  name: 'Team chat',
  url: 'https://mcp.example.org/chat',
  authMode: 'user',
  enabled: true,
  oauth: { clientId: 'client-1', secretStored: true, registration: 'static', authorizeUrl: '', tokenUrl: '', revokeUrl: '' },
  scopes: ['channels:read', 'chat:write'],
  allowedGroups: ['sales'],
  toolModes: { list_channels: 'read' },
  tools: [
    { name: 'list_channels', description: 'List channels', readOnlyHint: true },
    { name: 'post_message', description: 'Post a message', readOnlyHint: false },
  ],
  redirectUri: REDIRECT,
  updatedAt: '2026-11-20T10:00:00Z',
}

const wiki = {
  ...chat,
  id: 'c-2',
  rev: 'r-7',
  catalogId: '',
  slug: 'wiki',
  name: 'Wiki',
  url: 'https://wiki.example.org/mcp',
  authMode: 'shared',
  enabled: false,
  oauth: { clientId: '', secretStored: false, registration: '', authorizeUrl: '', tokenUrl: '', revokeUrl: '' },
  scopes: [],
  allowedGroups: [],
  toolModes: {},
  tools: [],
}

const catalog = {
  items: [
    {
      id: 'chat',
      slug: 'chat',
      name: 'Team chat',
      url: 'https://mcp.example.org/chat',
      authMode: 'user',
      scopes: ['channels:read'],
      toolModes: { post_message: 'write' },
      oauthEndpoints: 'discovered',
      oauth: {},
      setup: 'Create an OAuth app at the vendor and paste its client id and secret.',
      docs: 'https://docs.example.org/chat-mcp',
    },
    {
      id: 'tickets',
      slug: 'tickets',
      name: 'Tickets',
      url: '',
      authMode: 'shared',
      scopes: [],
      toolModes: {},
      oauthEndpoints: 'static',
      oauth: { authorizeUrl: 'https://tickets.example.org/oauth/authorize', tokenUrl: 'https://tickets.example.org/oauth/token' },
      setup: 'Fill in your own address.',
      docs: '',
    },
  ],
}

function api(routes: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: adminMe },
    'GET /api/admin/connectors': { body: { items: [chat, wiki] } },
    'GET /api/admin/connectors/catalog': { body: catalog },
    ...routes,
  })
}

const body = (m: ReturnType<typeof mockApi>, method: string) => m.calls.filter((c) => c.method === method).at(-1)?.body as Record<string, unknown>

describe('Connectors (admin)', () => {
  it('is not there for members', async () => {
    const m = mockApi({ 'GET /api/me': { body: me }, 'GET /api/secrets': { body: { items: [] } } })
    renderApp('/settings/connectors')
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeTruthy()
    expect(screen.queryByRole('link', { name: 'Connectors' })).toBeNull()
    expect(m.count('GET', '/api/admin/connectors')).toBe(0)
  })

  it('lists every connector with whether it is on and who signs in', async () => {
    api()
    renderApp('/settings/connectors')
    expect((await screen.findByRole('link', { name: 'Connectors' })).getAttribute('aria-current')).toBe('page')
    const list = await screen.findByRole('table', { name: 'Connectors' })
    const rows = within(list).getAllByRole('row').slice(1)
    // Catalogue order first (set up or not), then the custom ones, one per row.
    expect(rows).toHaveLength(3)
    expect(rows[0].textContent).toContain('Team chat')
    expect(rows[0].textContent).toContain('Each person signs in')
    expect(within(rows[0]).getByRole('switch', { name: 'Turn on Team chat' }).getAttribute('aria-checked')).toBe('true')
    expect(rows[1].textContent).toContain('Tickets')
    expect(rows[1].textContent).toContain('Not set up')
    expect(within(rows[1]).queryByRole('switch')).toBeNull()
    expect(within(rows[1]).getByRole('button', { name: 'Set up Tickets' })).toBeTruthy()
    expect(rows[2].textContent).toContain('One shared account')
    expect(within(rows[2]).getByRole('switch', { name: 'Turn on Wiki' }).getAttribute('aria-checked')).toBe('false')
  })

  it('shows each catalogue connector with its vendor logo and a custom one with a plug', async () => {
    const github = { ...chat, catalogId: 'github', slug: 'github', name: 'GitHub' }
    api({
      'GET /api/admin/connectors': { body: { items: [github, wiki] } },
      'GET /api/admin/connectors/catalog': { body: { items: [{ ...catalog.items[0], id: 'github', slug: 'github', name: 'GitHub' }] } },
    })
    renderApp('/settings/connectors')
    const rows = within(await screen.findByRole('table', { name: 'Connectors' })).getAllByRole('row').slice(1)
    expect(rows[0].textContent).toContain('GitHub')
    expect(rows[0].querySelector('.mark')).not.toBeNull()
    expect(rows[1].textContent).toContain('Wiki')
    expect(rows[1].querySelector('svg.lucide-plug')).not.toBeNull()
  })

  it('says so when connectors are off on the platform', async () => {
    api({
      'GET /api/admin/connectors': { status: 409, body: { status: 409, title: 'Connectors are off on this platform (connectors.enabled in the platform config).' } },
    })
    renderApp('/settings/connectors')
    expect(await screen.findByText(/Connectors are off on this platform/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Add connector' })).toBeNull()
  })

  it('adds one from the catalogue with the fields it prefills', async () => {
    const m = api({
      'GET /api/admin/connectors': { body: { items: [wiki] } },
      'POST /api/admin/connectors': (c: Call) => ({ status: 201, body: { ...chat, id: 'c-9', ...(c.body as object) } }),
    })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Set up Team chat' }))
    const form = await screen.findByRole('form', { name: 'Add Team chat' })
    expect(within(form).getByText(/Create an OAuth app at the vendor/)).toBeTruthy()
    expect((within(form).getByLabelText('Address of the MCP server') as HTMLInputElement).value).toBe('https://mcp.example.org/chat')
    expect(within(form).getByText(REDIRECT)).toBeTruthy()
    await userEvent.type(within(form).getByLabelText('OAuth client id'), 'client-9')
    await userEvent.type(within(form).getByLabelText('OAuth client secret'), 'shh-9')
    await userEvent.type(within(form).getByLabelText('Allowed groups'), 'sales, *')
    await userEvent.click(within(form).getByRole('button', { name: 'Add connector' }))
    await vi.waitFor(() => expect(m.count('POST', '/api/admin/connectors')).toBe(1))
    expect(body(m, 'POST')).toEqual({
      catalogId: 'chat',
      slug: 'chat',
      name: 'Team chat',
      url: 'https://mcp.example.org/chat',
      authMode: 'user',
      enabled: true,
      oauth: { clientId: 'client-9', clientSecret: 'shh-9' },
      scopes: ['channels:read'],
      allowedGroups: ['sales', '*'],
      toolModes: { post_message: 'write' },
    })
    expect(await screen.findByRole('table', { name: 'Connectors' })).toBeTruthy()
    expect(document.body.textContent).not.toContain('shh-9')
  })

  it('adds a custom MCP server by its address', async () => {
    const m = api({ 'POST /api/admin/connectors': (c: Call) => ({ status: 201, body: { ...wiki, ...(c.body as object) } }) })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Add a custom MCP server' }))
    const form = await screen.findByRole('form', { name: 'Add a custom connector' })
    await userEvent.type(within(form).getByLabelText('Name'), 'Docs')
    await userEvent.type(within(form).getByLabelText('Short name'), 'docs')
    await userEvent.type(within(form).getByLabelText('Address of the MCP server'), 'https://docs.example.org/mcp')
    await userEvent.click(within(form).getByRole('radio', { name: 'One shared account' }))
    await userEvent.click(within(form).getByRole('button', { name: 'Add connector' }))
    await vi.waitFor(() => expect(m.count('POST', '/api/admin/connectors')).toBe(1))
    expect(body(m, 'POST')).toEqual({
      slug: 'docs',
      name: 'Docs',
      url: 'https://docs.example.org/mcp',
      authMode: 'shared',
      enabled: true,
      oauth: { clientId: '' },
      scopes: [],
      allowedGroups: [],
      toolModes: {},
    })
  })

  it('never shows the stored client secret, and keeps it when the field is left empty', async () => {
    const m = api({ 'PUT /api/admin/connectors/c-1': (c: Call) => ({ body: { ...chat, ...(c.body as object), rev: 'r-2' } }) })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Team chat' }))
    const form = await screen.findByRole('form', { name: 'Team chat' })
    const secret = within(form).getByLabelText('OAuth client secret') as HTMLInputElement
    expect(secret.value).toBe('')
    expect(secret.type).toBe('password')
    expect(within(form).getByText(/A secret is stored/)).toBeTruthy()
    const name = within(form).getByLabelText('Name')
    await userEvent.clear(name)
    await userEvent.type(name, 'Chat')
    await userEvent.click(within(form).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/connectors/c-1')).toBe(1))
    const put = body(m, 'PUT')
    expect(put.rev).toBe('r-1')
    expect(put.name).toBe('Chat')
    expect(put.oauth).toEqual({ clientId: 'client-1' })

    await userEvent.click(await screen.findByRole('button', { name: 'Edit Team chat' }))
    const again = await screen.findByRole('form', { name: 'Team chat' })
    await userEvent.type(within(again).getByLabelText('OAuth client secret'), 'new-secret')
    await userEvent.click(within(again).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/connectors/c-1')).toBe(2))
    expect(body(m, 'PUT').oauth).toEqual({ clientId: 'client-1', clientSecret: 'new-secret' })
  })

  it('saves the mode picked for each tool, and a tool added by name', async () => {
    const m = api({ 'PUT /api/admin/connectors/c-1': (c: Call) => ({ body: { ...chat, ...(c.body as object), rev: 'r-2' } }) })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Team chat' }))
    const form = await screen.findByRole('form', { name: 'Team chat' })
    const tools = within(form).getByRole('table', { name: 'Tools' })
    expect(within(tools).getByRole('radiogroup', { name: 'list_channels' }).querySelector('[aria-checked="true"]')?.textContent).toBe('Read')
    // Not set yet: counts as a write.
    expect(within(tools).getByRole('radiogroup', { name: 'post_message' }).querySelector('[aria-checked="true"]')?.textContent).toBe('Write')
    await userEvent.click(within(within(tools).getByRole('radiogroup', { name: 'post_message' })).getByRole('radio', { name: 'Off' }))
    await userEvent.type(within(form).getByLabelText('Add a tool by name'), 'delete_channel')
    await userEvent.click(within(form).getByRole('button', { name: 'Add tool' }))
    await userEvent.click(within(within(tools).getByRole('radiogroup', { name: 'delete_channel' })).getByRole('radio', { name: 'Off' }))
    await userEvent.click(within(form).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/connectors/c-1')).toBe(1))
    expect(body(m, 'PUT').toolModes).toEqual({ list_channels: 'read', post_message: 'off', delete_channel: 'off' })
  })

  it('says the tool list is empty until Tapas reads it from the server', async () => {
    api()
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Wiki' }))
    const form = await screen.findByRole('form', { name: 'Wiki' })
    expect(within(form).getByText(/No tools are known yet/)).toBeTruthy()
    expect(within(form).queryByRole('table', { name: 'Tools' })).toBeNull()
  })

  it('turns a connector on from the list', async () => {
    const m = api({ 'PUT /api/admin/connectors/c-2': (c: Call) => ({ body: { ...wiki, ...(c.body as object), rev: 'r-8' } }) })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('switch', { name: 'Turn on Wiki' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/connectors/c-2')).toBe(1))
    expect(body(m, 'PUT')).toEqual({
      slug: 'wiki',
      name: 'Wiki',
      url: 'https://wiki.example.org/mcp',
      authMode: 'shared',
      enabled: true,
      oauth: { clientId: '' },
      scopes: [],
      allowedGroups: [],
      toolModes: {},
      rev: 'r-7',
    })
  })

  it('removes a connector after asking', async () => {
    const m = api({ 'DELETE /api/admin/connectors/c-2': { status: 204 } })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Wiki' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Remove connector' }))
    const ask = await screen.findByRole('alertdialog', { name: 'Remove Wiki?' })
    expect(m.count('DELETE', '/api/admin/connectors/c-2')).toBe(0)
    await userEvent.click(within(ask).getByRole('button', { name: 'Remove' }))
    await vi.waitFor(() => expect(m.count('DELETE', '/api/admin/connectors/c-2')).toBe(1))
    await vi.waitFor(() => expect(screen.queryByRole('form', { name: 'Wiki' })).toBeNull())
  })

  it('says someone else saved first, and reloads the connector', async () => {
    const changed = { ...chat, rev: 'r-2', name: 'Chat by bob' }
    const m = api({
      'GET /api/admin/connectors': [{ body: { items: [chat, wiki] } }, { body: { items: [changed, wiki] } }],
      'PUT /api/admin/connectors/c-1': {
        status: 409,
        body: { status: 409, title: 'Someone else saved this connector in the meantime. Reload it and make your change again.' },
      },
    })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Team chat' }))
    const form = await screen.findByRole('form', { name: 'Team chat' })
    await userEvent.click(within(form).getByRole('button', { name: 'Save' }))
    expect(await screen.findByText(/Someone else saved this connector/)).toBeTruthy()
    const fresh = await screen.findByRole('form', { name: 'Chat by bob' })
    expect((within(fresh).getByLabelText('Name') as HTMLInputElement).value).toBe('Chat by bob')
    expect(screen.getByText(/Someone else saved this connector/)).toBeTruthy()
    await userEvent.click(within(fresh).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/connectors/c-1')).toBe(2))
    expect(body(m, 'PUT').rev).toBe('r-2')
  })

  it('says how many people connected each connector', async () => {
    api({ 'GET /api/admin/connectors': { body: { items: [{ ...chat, connections: 3 }, wiki] } } })
    renderApp('/settings/connectors')
    const rows = within(await screen.findByRole('table', { name: 'Connectors' })).getAllByRole('row').slice(1)
    expect(rows[0].textContent).toContain('3 people connected')
  })

  it('connects the shared account by sending the admin to the vendor', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, origin: window.location.origin, assign })
    const signIn = 'https://wiki.example.org/oauth/authorize?state=s-9'
    const m = api({
      'GET /api/admin/connectors': { body: { items: [chat, { ...wiki, shared: { connected: false } }] } },
      'POST /api/admin/connectors/c-2/connect': { body: { authorizeUrl: signIn } },
    })
    renderApp('/settings/connectors')
    const rows = within(await screen.findByRole('table', { name: 'Connectors' })).getAllByRole('row').slice(1)
    expect(rows[2].textContent).toContain('Shared account not connected')
    await userEvent.click(within(rows[2]).getByRole('button', { name: 'Connect the shared account for Wiki' }))
    await vi.waitFor(() => expect(assign).toHaveBeenCalledWith(signIn))
    expect(m.count('POST', '/api/admin/connectors/c-2/connect')).toBe(1)
  })

  it('disconnects the shared account after asking', async () => {
    const shared = { ...wiki, connections: 1, shared: { connected: true, connectedBy: 'alice', status: 'active' } }
    const m = api({
      'GET /api/admin/connectors': [{ body: { items: [chat, shared] } }, { body: { items: [chat, { ...wiki, shared: { connected: false } }] } }],
      'DELETE /api/admin/connectors/c-2/connection': { status: 204 },
    })
    renderApp('/settings/connectors')
    const rows = within(await screen.findByRole('table', { name: 'Connectors' })).getAllByRole('row').slice(1)
    expect(rows[2].textContent).toContain('Shared account connected by alice')
    expect(within(rows[2]).queryByRole('button', { name: /Connect the shared account/ })).toBeNull()
    await userEvent.click(within(rows[2]).getByRole('button', { name: 'Disconnect the shared account for Wiki' }))
    const ask = await screen.findByRole('alertdialog', { name: 'Disconnect the shared account for Wiki?' })
    expect(m.count('DELETE', '/api/admin/connectors/c-2/connection')).toBe(0)
    await userEvent.click(within(ask).getByRole('button', { name: 'Disconnect' }))
    await vi.waitFor(() => expect(m.count('DELETE', '/api/admin/connectors/c-2/connection')).toBe(1))
    expect(await screen.findByText('Shared account not connected')).toBeTruthy()
  })

  it('offers to reconnect a shared account the vendor no longer accepts', async () => {
    api({ 'GET /api/admin/connectors': { body: { items: [{ ...wiki, shared: { connected: true, connectedBy: 'alice', status: 'needs_reauth' } }] } } })
    renderApp('/settings/connectors')
    expect(await screen.findByText(/Shared account needs reconnecting/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Reconnect the shared account for Wiki' })).toBeTruthy()
  })

  it('refreshes the tool list from the server, keeping what was typed, and saves at the new rev', async () => {
    const refreshed = {
      ...chat,
      rev: 'r-5',
      tools: [...chat.tools, { name: 'archive_channel', description: 'Archive a channel', readOnlyHint: false }],
    }
    const m = api({
      'POST /api/admin/connectors/c-1/tools/refresh': { body: refreshed },
      'PUT /api/admin/connectors/c-1': (c: Call) => ({ body: { ...refreshed, ...(c.body as object), rev: 'r-6' } }),
    })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Team chat' }))
    const form = await screen.findByRole('form', { name: 'Team chat' })
    const name = within(form).getByLabelText('Name')
    await userEvent.clear(name)
    await userEvent.type(name, 'Chat')
    await userEvent.click(within(form).getByRole('button', { name: 'Refresh tools' }))
    await vi.waitFor(() => expect(m.count('POST', '/api/admin/connectors/c-1/tools/refresh')).toBe(1))
    const again = await screen.findByRole('form', { name: 'Team chat' })
    const tools = within(again).getByRole('table', { name: 'Tools' })
    expect(await within(tools).findByText('archive_channel')).toBeTruthy()
    expect((within(again).getByLabelText('Name') as HTMLInputElement).value).toBe('Chat')
    await userEvent.click(within(again).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/connectors/c-1')).toBe(1))
    expect(body(m, 'PUT').rev).toBe('r-5')
    expect(body(m, 'PUT').toolModes).toEqual({ list_channels: 'read', post_message: 'write', archive_channel: 'write' })
  })

  it('says why the tools could not be refreshed', async () => {
    api({
      'POST /api/admin/connectors/c-1/tools/refresh': {
        status: 409,
        body: { status: 409, title: 'You have not connected Team chat yet. Connect it under Connected accounts first.' },
      },
    })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Team chat' }))
    const form = await screen.findByRole('form', { name: 'Team chat' })
    await userEvent.click(within(form).getByRole('button', { name: 'Refresh tools' }))
    expect(await within(form).findByText(/You have not connected Team chat yet/)).toBeTruthy()
  })
})
