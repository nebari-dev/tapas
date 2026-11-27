import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { MemoryView } from '../../api'
import { mockApi, renderApp } from '../../test/api'
import { me } from '../../test/fixtures'

const likesGo: MemoryView = { id: 'm1', text: 'Prefers Go over Python for services', createdAt: '2026-11-27T09:00:00Z', updatedAt: '2026-11-27T09:00:00Z' }
const staging: MemoryView = { id: 'm2', text: 'Deploys go to the staging cluster first', createdAt: '2026-11-20T09:00:00Z', updatedAt: '2026-11-21T09:00:00Z' }

function setup(routes: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/memories': { body: [likesGo, staging] },
    ...routes,
  })
}

describe('Settings, Memories', () => {
  it('lists what is remembered with the day each memory was saved', async () => {
    setup()
    renderApp('/settings/memories')
    const list = await screen.findByRole('list', { name: 'Memories' })
    const rows = within(list).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('Prefers Go over Python for services')).toBeTruthy()
    expect(within(rows[0]).getByText('27 Nov 2026')).toBeTruthy()
    expect(within(rows[1]).getByText('Deploys go to the staging cluster first')).toBeTruthy()
    expect(screen.getByText(/What your coding agents remember about you/)).toBeTruthy()
  })

  it('says nothing is remembered yet and how to add a memory', async () => {
    setup({ 'GET /api/memories': { body: [] } })
    renderApp('/settings/memories')
    expect(await screen.findByText('Nothing remembered yet.')).toBeTruthy()
    expect(screen.getAllByText(/remember that/).length).toBeGreaterThan(0)
    expect(screen.queryByRole('list', { name: 'Memories' })).toBeNull()
  })

  it('shows a banner and no list when memory is off on the platform', async () => {
    setup({ 'GET /api/memories': { status: 404, body: { title: 'Memory is off on this platform.', status: 404 } } })
    renderApp('/settings/memories')
    expect(await screen.findByText('Memory is off on this platform.')).toBeTruthy()
    expect(screen.queryByRole('list', { name: 'Memories' })).toBeNull()
  })

  it('deletes a memory after confirming and drops its row', async () => {
    const api = setup({
      'DELETE /api/memories/m1': { status: 204 },
      'GET /api/memories': [{ body: [likesGo, staging] }, { body: [staging] }],
    })
    renderApp('/settings/memories')
    const list = await screen.findByRole('list', { name: 'Memories' })
    await userEvent.click(within(list).getAllByRole('button', { name: /^Delete/ })[0])
    await userEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(screen.queryByText('Prefers Go over Python for services')).toBeNull())
    expect(api.calls.some((c) => c.method === 'DELETE' && c.path === '/api/memories/m1')).toBe(true)
    expect(screen.getByText('Deploys go to the staging cluster first')).toBeTruthy()
  })
})
