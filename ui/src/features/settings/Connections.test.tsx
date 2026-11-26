import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { me } from '../../test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

const chat = {
  id: 'c-1',
  slug: 'chat',
  name: 'Team chat',
  authMode: 'user',
  connected: true,
  status: 'active',
  connectedAt: '2026-11-20T10:00:00Z',
  connectUrl: 'https://tapas.example.org/settings/connections',
  tools: [
    { name: 'chat__list_channels', mode: 'read' },
    { name: 'chat__post_message', mode: 'write' },
  ],
}
const tickets = { ...chat, id: 'c-2', slug: 'tickets', name: 'Tickets', connected: false, status: undefined, connectedAt: undefined }
const docs = { ...chat, id: 'c-3', slug: 'docs', name: 'Docs', status: 'needs_reauth' }
const wiki = { ...chat, id: 'c-4', slug: 'wiki', name: 'Wiki', authMode: 'shared' }
const crm = { ...tickets, id: 'c-5', slug: 'crm', name: 'CRM', authMode: 'shared' }

function api(routes: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/connectors': { body: { items: [chat, tickets, docs, wiki, crm] } },
    ...routes,
  })
}

const row = async (name: string) => (await screen.findByRole('listitem', { name })) as HTMLElement

describe('Connected accounts', () => {
  it('lists every connector the person may use, one row each, with its status', async () => {
    api()
    renderApp('/settings/connections')
    expect((await screen.findByRole('link', { name: 'Connected accounts' })).getAttribute('aria-current')).toBe('page')
    const list = await screen.findByRole('list', { name: 'Connected accounts' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(5)
    expect((await row('Team chat')).textContent).toContain('Connected since 20 Nov 2026')
    expect((await row('Team chat')).textContent).toContain('Tools: list_channels; post_message asks you first')
    expect(within(await row('Team chat')).getByRole('button', { name: 'Disconnect Team chat' })).toBeTruthy()
    expect((await row('Tickets')).textContent).toContain('Not connected')
    expect(within(await row('Tickets')).getByRole('button', { name: 'Connect Tickets' })).toBeTruthy()
    expect((await row('Docs')).textContent).toContain('Needs reconnecting')
    expect(within(await row('Docs')).getByRole('button', { name: 'Reconnect Docs' })).toBeTruthy()
  })

  it('shows a company-wide connector without a button: the admin connects it', async () => {
    api()
    renderApp('/settings/connections')
    const w = await row('Wiki')
    expect(w.textContent).toContain('Connected by your admin')
    expect(within(w).queryByRole('button')).toBeNull()
    const c = await row('CRM')
    expect(c.textContent).toContain('Not connected yet, ask your admin')
    expect(within(c).queryByRole('button')).toBeNull()
  })

  it('says so when there is nothing to connect', async () => {
    api({ 'GET /api/connectors': { body: { items: [] } } })
    renderApp('/settings/connections')
    expect(await screen.findByText('No connectors yet')).toBeTruthy()
  })

  it('sends the browser to the vendor to connect', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, origin: window.location.origin, assign })
    const signIn = 'https://tickets.example.org/oauth/authorize?state=s-1'
    const m = api({ 'POST /api/connectors/c-2/connect': { body: { authorizeUrl: signIn } } })
    renderApp('/settings/connections')
    await userEvent.click(within(await row('Tickets')).getByRole('button', { name: 'Connect Tickets' }))
    await vi.waitFor(() => expect(assign).toHaveBeenCalledWith(signIn))
    expect(m.count('POST', '/api/connectors/c-2/connect')).toBe(1)
  })

  it('shows why connecting could not start', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, origin: window.location.origin, assign })
    api({ 'POST /api/connectors/c-2/connect': { status: 502, body: { status: 502, title: 'Tickets did not answer. Try again in a minute.' } } })
    renderApp('/settings/connections')
    await userEvent.click(within(await row('Tickets')).getByRole('button', { name: 'Connect Tickets' }))
    expect(await screen.findByText('Tickets did not answer. Try again in a minute.')).toBeTruthy()
    expect(assign).not.toHaveBeenCalled()
  })

  it('disconnects only after asking', async () => {
    const m = api({
      'GET /api/connectors': [{ body: { items: [chat] } }, { body: { items: [{ ...chat, connected: false, status: undefined, connectedAt: undefined }] } }],
      'DELETE /api/connectors/c-1/connection': { status: 204 },
    })
    renderApp('/settings/connections')
    await userEvent.click(within(await row('Team chat')).getByRole('button', { name: 'Disconnect Team chat' }))
    const ask = await screen.findByRole('alertdialog', { name: 'Disconnect Team chat?' })
    expect(m.count('DELETE', '/api/connectors/c-1/connection')).toBe(0)
    await userEvent.click(within(ask).getByRole('button', { name: 'Disconnect' }))
    await vi.waitFor(() => expect(m.count('DELETE', '/api/connectors/c-1/connection')).toBe(1))
    await vi.waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(await within(await row('Team chat')).findByRole('button', { name: 'Connect Team chat' })).toBeTruthy()
  })
})
