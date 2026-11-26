import { Link2, Unlink } from 'lucide-react'
import { useRef, useState } from 'react'
import type { MyConnector } from '../../api'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { Confirm } from '../../components/Confirm'
import { EmptyState } from '../../components/EmptyState'
import { ConnectorLogo } from '../../components/KindLogo'
import { problemText } from '../../lib/http'
import { useConnectAccount, useDisconnectAccount, useMyConnectors } from '../../lib/queries'

/** "2 Nov 2026". */
function day(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric' }).format(d)
}

/** What the person sees as the connection's state, and its tone. */
function statusOf(c: MyConnector): { text: string; tone: 'ok' | 'warn' | 'off' } {
  if (c.authMode === 'shared') return c.connected ? { text: 'Connected by your admin', tone: 'ok' } : { text: 'Not connected yet, ask your admin', tone: 'off' }
  if (!c.connected) return { text: 'Not connected', tone: 'off' }
  if (c.status === 'needs_reauth') return { text: 'Needs reconnecting: the service no longer accepts it', tone: 'warn' }
  return { text: c.connectedAt ? `Connected since ${day(c.connectedAt)}` : 'Connected', tone: 'ok' }
}

/** "Tools: a, b; c asks you first", with each tool's name as the connector's server calls it. */
function toolsLine(c: MyConnector): string {
  const tools = c.tools ?? []
  if (tools.length === 0) return ''
  const bare = (n: string) => (n.startsWith(`${c.slug}__`) ? n.slice(c.slug.length + 2) : n)
  const reads = tools.filter((t) => t.mode === 'read').map((t) => bare(t.name))
  const writes = tools.filter((t) => t.mode === 'write').map((t) => bare(t.name))
  const parts = [reads.join(', '), writes.length ? `${writes.join(', ')} ${writes.length === 1 ? 'asks' : 'ask'} you first` : ''].filter(Boolean)
  return `Tools: ${parts.join('; ')}`
}

function ConnectionRow({ c, onError }: { c: MyConnector; onError: (msg: string) => void }) {
  const connect = useConnectAccount()
  const disconnect = useDisconnectAccount()
  const [asking, setAsking] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  const st = statusOf(c)
  const tools = toolsLine(c)
  const go = () => {
    onError('')
    connect.mutate(c.id, { onSuccess: (url) => window.location.assign(url), onError: (err) => onError(problemText(err)) })
  }
  const user = c.authMode === 'user'
  return (
    <li className="con-row" aria-label={c.name}>
      <ConnectorLogo catalogId={c.catalogId} />
      <div className="con-main">
        <h3 className="con-name">{c.name}</h3>
        <span className="faint small">{user ? 'You sign in with your own account' : 'One account for the whole company'}</span>
        <span className={`con-status ${st.tone}`}>{st.text}</span>
        {tools && <span className="faint small con-tools-line">{tools}</span>}
      </div>
      {user && (
        <div className="con-row-act row">
          {c.connected && (
            <Button ref={btn} size="sm" variant="ghost" icon={Unlink} aria-label={`Disconnect ${c.name}`} onClick={() => setAsking(true)}>
              Disconnect
            </Button>
          )}
          {(!c.connected || c.status === 'needs_reauth') && (
            <Button
              size="sm"
              variant={c.connected ? 'primary' : undefined}
              icon={Link2}
              aria-label={`${c.connected ? 'Reconnect' : 'Connect'} ${c.name}`}
              disabled={connect.isPending}
              onClick={go}
            >
              {c.connected ? 'Reconnect' : 'Connect'}
            </Button>
          )}
        </div>
      )}
      {asking && (
        <Confirm
          title={`Disconnect ${c.name}?`}
          body={`Your agents stop using ${c.name} as you at once. Tapas asks ${c.name} to forget the sign-in and deletes it here. You can connect again any time.`}
          confirm="Disconnect"
          icon={Unlink}
          danger
          busy={disconnect.isPending}
          error={disconnect.isError ? disconnect.error : undefined}
          onConfirm={() => disconnect.mutate(c.id, { onSuccess: () => setAsking(false) })}
          onCancel={() => {
            setAsking(false)
            btn.current?.focus()
          }}
        />
      )}
    </li>
  )
}

/** Settings, Connected accounts: every connector the person may use, and their own sign-in to each. */
export function Connections() {
  const q = useMyConnectors()
  const [error, setError] = useState('')
  const list = q.data ?? []
  return (
    <section className="section" aria-label="Connected accounts">
      <div className="section-head tok-head">
        <p className="sub tok-intro">
          Connect your accounts at other services so your agents can use them as you, through Tapas. Agents never see your password or
          token, and anything that changes something waits for you to approve it.
        </p>
      </div>
      {error && (
        <div className="section-sm">
          <Banner tone="err" title={error} />
        </div>
      )}
      <div className="section-sm">
        {q.isError ? (
          <Banner tone="err" title={problemText(q.error)} />
        ) : q.isPending ? (
          <div className="muted">Loading</div>
        ) : list.length === 0 ? (
          <EmptyState title="No connectors yet" body="When an admin adds a service you may use, such as Slack or GitHub, it shows here to connect." />
        ) : (
          <Card>
            <ul className="con-list" aria-label="Connected accounts">
              {list.map((c) => (
                <ConnectionRow key={c.id} c={c} onError={setError} />
              ))}
            </ul>
          </Card>
        )}
      </div>
    </section>
  )
}
