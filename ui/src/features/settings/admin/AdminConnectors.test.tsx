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
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('Team chat')
    expect(rows[0].textContent).toContain('Each person signs in')
    expect(within(rows[0]).getByRole('switch', { name: 'Turn on Team chat' }).getAttribute('aria-checked')).toBe('true')
    expect(rows[1].textContent).toContain('One shared account')
    expect(within(rows[1]).getByRole('switch', { name: 'Turn on Wiki' }).getAttribute('aria-checked')).toBe('false')
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
    const m = api({ 'POST /api/admin/connectors': (c: Call) => ({ status: 201, body: { ...chat, id: 'c-9', ...(c.body as object) } }) })
    renderApp('/settings/connectors')
    await userEvent.click(await screen.findByRole('button', { name: 'Add connector' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Team chat' }))
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
    await userEvent.click(await screen.findByRole('button', { name: 'Add connector' }))
    await userEvent.click(await screen.findByRole('button', { name: /Custom MCP URL/ }))
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
})
