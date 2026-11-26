import { screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { me } from '../../test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

const tickets = {
  id: 'c-2',
  slug: 'tickets',
  name: 'Tickets',
  authMode: 'user',
  connected: true,
  status: 'active',
  connectedAt: '2026-11-26T10:00:00Z',
  connectUrl: 'https://tapas.example.org/settings/connections',
  tools: [],
}

const CALLBACK = '/settings/connections/callback/tickets?state=s-1&code=k-1&iss=https%3A%2F%2Fauth.example.org'

describe('Connection callback', () => {
  it('finishes the sign-in once, takes the code out of the address, and says it worked', async () => {
    const m = mockApi({
      'GET /api/me': { body: me },
      'POST /api/connectors/oauth/complete': { body: { connector: tickets } },
    })
    renderApp(CALLBACK)
    expect(await screen.findByText('Tickets is connected')).toBeTruthy()
    expect(m.count('POST', '/api/connectors/oauth/complete')).toBe(1)
    expect(m.calls.find((c) => c.method === 'POST')?.body).toEqual({ slug: 'tickets', state: 's-1', code: 'k-1', iss: 'https://auth.example.org' })
    expect(location()).toBe('/settings/connections/callback/tickets')
    expect(screen.getByRole('link', { name: 'Back to Connected accounts' }).getAttribute('href')).toBe('/settings/connections')
  })

  it('posts once under StrictMode too', async () => {
    const m = mockApi({ 'GET /api/me': { body: me }, 'POST /api/connectors/oauth/complete': { body: { connector: tickets } } })
    renderApp(CALLBACK, { strict: true })
    expect(await screen.findByText('Tickets is connected')).toBeTruthy()
    await new Promise((r) => setTimeout(r, 50))
    expect(m.count('POST', '/api/connectors/oauth/complete')).toBe(1)
  })

  it("shows the server's reason when the sign-in cannot be finished", async () => {
    mockApi({
      'GET /api/me': { body: me },
      'POST /api/connectors/oauth/complete': { status: 400, body: { status: 400, title: 'This sign-in expired. Connect again from Connected accounts.' } },
    })
    renderApp(CALLBACK)
    expect(await screen.findByText('This sign-in expired. Connect again from Connected accounts.')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Back to Connected accounts' })).toBeTruthy()
  })

  it('says the vendor refused, and sends nothing, when the vendor answered with an error', async () => {
    const m = mockApi({ 'GET /api/me': { body: me } })
    renderApp('/settings/connections/callback/tickets?state=s-1&error=access_denied&error_description=The+user+said+no')
    expect(await screen.findByText(/The service did not connect your account/)).toBeTruthy()
    // Text from the address is anyone's to write, so it is not shown.
    expect(document.body.textContent).not.toContain('The user said no')
    expect(m.count('POST', '/api/connectors/oauth/complete')).toBe(0)
    expect(location()).toBe('/settings/connections/callback/tickets')
  })

  it('sends a company-wide sign-in back to the Connectors page', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'POST /api/connectors/oauth/complete': { body: { connector: { ...tickets, authMode: 'shared' } } } })
    renderApp(CALLBACK)
    expect(await screen.findByText('Tickets is connected')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Back to Connectors' }).getAttribute('href')).toBe('/settings/connectors')
  })
})
