import { ExternalLink, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useId, useState, type FormEvent, type ReactNode } from 'react'
import type { AdminConnector, CatalogEntry, ConnectorInput } from '../../../api'
import { Banner } from '../../../components/Banner'
import { Button } from '../../../components/Button'
import { Card } from '../../../components/Card'
import { Confirm } from '../../../components/Confirm'
import { CopyButton } from '../../../components/CopyButton'
import { Segmented } from '../../../components/Segmented'
import { Switch } from '../../../components/Switch'
import { ApiError, fieldErrors, problemText } from '../../../lib/http'
import { ConnectorLogo } from '../../../components/KindLogo'
import {
  useConnectorCatalog,
  useConnectors,
  useCreateConnector,
  useDeleteConnector,
  useIsAdmin,
  useRefreshConnectorTools,
  useReloadConnectors,
  useUpdateConnector,
} from './queries'

const TOOL_MODES = [
  { value: 'read', label: 'Read' },
  { value: 'write', label: 'Write' },
  { value: 'off', label: 'Off' },
] as const

type ToolMode = (typeof TOOL_MODES)[number]['value']

/** A tool the connector has not set is a write (the server's rule). */
const DEFAULT_MODE: ToolMode = 'write'

const isConflict = (err: unknown) => err instanceof ApiError && err.status === 409

/** The form's fields; lists are kept as the text typed. */
interface Draft {
  catalogId: string
  slug: string
  name: string
  url: string
  enabled: boolean
  clientId: string
  authorizeUrl: string
  tokenUrl: string
  revokeUrl: string
  scopes: string
  groups: string
  modes: Record<string, string>
}

const words = (s: string) => s.split(/[\s,]+/).filter(Boolean)

function draftOf(c: AdminConnector): Draft {
  return {
    catalogId: c.catalogId,
    slug: c.slug,
    name: c.name,
    url: c.url,
    enabled: c.enabled,
    clientId: c.oauth.clientId,
    authorizeUrl: c.oauth.authorizeUrl ?? '',
    tokenUrl: c.oauth.tokenUrl ?? '',
    revokeUrl: c.oauth.revokeUrl ?? '',
    scopes: (c.scopes ?? []).join(' '),
    groups: (c.allowedGroups ?? []).join(', '),
    modes: { ...c.toolModes },
  }
}

function draftFrom(e: CatalogEntry | null): Draft {
  return {
    catalogId: e?.id ?? '',
    slug: e?.slug ?? '',
    name: e?.name ?? '',
    url: e?.url ?? '',
    enabled: true,
    clientId: '',
    authorizeUrl: e?.oauth.authorizeUrl ?? '',
    tokenUrl: e?.oauth.tokenUrl ?? '',
    revokeUrl: e?.oauth.revokeUrl ?? '',
    scopes: (e?.scopes ?? []).join(' '),
    groups: '',
    modes: { ...e?.toolModes },
  }
}

/**
 * d with a mode for every tool the server listed: what the admin set, else
 * read for a tool the server says only reads, else write. The server's word
 * only prefills; what is saved is what the admin sees here.
 */
function withTools(d: Draft, tools: AdminConnector['tools']): Draft {
  const modes = { ...d.modes }
  for (const t of tools ?? []) modes[t.name] ??= t.readOnlyHint ? 'read' : DEFAULT_MODE
  return { ...d, modes }
}

/**
 * The request body for d. A PUT replaces the whole connector, so the OAuth
 * endpoints go back as read; the client secret goes only when one was typed.
 */
function inputOf(d: Draft, secret: string, rev?: string): ConnectorInput {
  const oauth: ConnectorInput['oauth'] = { clientId: d.clientId.trim() }
  if (secret) oauth.clientSecret = secret
  if (d.authorizeUrl) oauth.authorizeUrl = d.authorizeUrl
  if (d.tokenUrl) oauth.tokenUrl = d.tokenUrl
  if (d.revokeUrl) oauth.revokeUrl = d.revokeUrl
  return {
    ...(d.catalogId ? { catalogId: d.catalogId } : {}),
    slug: d.slug.trim(),
    name: d.name.trim(),
    url: d.url.trim(),
    enabled: d.enabled,
    oauth,
    scopes: words(d.scopes),
    allowedGroups: words(d.groups),
    toolModes: d.modes,
    ...(rev ? { rev } : {}),
  }
}

/** The redirect address with a copy button, or why there is none yet. */
function RedirectAddress({ uri, needsSlug }: { uri?: string; needsSlug?: boolean }) {
  if (uri === undefined) return <span className="hint">Loading</span>
  if (needsSlug) return <span className="hint">Shown here once the short name is filled in.</span>
  if (!uri) return <span className="hint">The platform config sets no public address, so there is no redirect address yet.</span>
  return (
    <div className="row con-redirect">
      <code className="mono">{uri}</code>
      <CopyButton text={() => uri} label="Copy the redirect address" />
    </div>
  )
}

/** A read, write or off control per tool, and a way to add a tool the server has not listed yet. */
function ToolModes({
  tools,
  modes,
  onChange,
  refresh,
}: {
  tools?: AdminConnector['tools']
  modes: Record<string, string>
  onChange: (m: Record<string, string>) => void
  /** The Refresh tools control, for a connector already saved. */
  refresh?: ReactNode
}) {
  const ids = { head: useId(), add: useId() }
  const [name, setName] = useState('')
  const about = new Map((tools ?? []).map((t) => [t.name, t]))
  const names = [...new Set([...about.keys(), ...Object.keys(modes)])]
  const add = () => {
    const n = name.trim()
    if (!n) return
    if (!(n in modes)) onChange({ ...modes, [n]: DEFAULT_MODE })
    setName('')
  }
  return (
    <div className="field" role="group" aria-labelledby={ids.head}>
      <span id={ids.head} className="field-label">
        Tools
      </span>
      <span className="hint">
        Read tools run at once. Write tools become plans the agent's owner approves first. Off tools are hidden from agents.
      </span>
      {refresh}
      {names.length === 0 ? (
        <p className="tight muted small">
          No tools are known yet: Tapas has not read the list from the server. Add a tool by name to set its mode now. A tool not set here
          counts as a write.
        </p>
      ) : (
        <div className="card atable-wrap">
          <table className="atable con-tools">
            <caption className="sr-only">Tools</caption>
            <thead>
              <tr>
                <th scope="col">Tool</th>
                <th scope="col">Mode</th>
              </tr>
            </thead>
            <tbody>
              {names.map((n) => {
                const t = about.get(n)
                return (
                  <tr key={n}>
                    <th scope="row">
                      <div className="atable-name">
                        <span className="mono">{n}</span>
                        {t?.description && <span className="faint small">{t.description}</span>}
                        {t?.readOnlyHint && <span className="faint small">The server says it only reads.</span>}
                      </div>
                    </th>
                    <td>
                      <Segmented
                        label={n}
                        value={(modes[n] as ToolMode | undefined) ?? DEFAULT_MODE}
                        options={[...TOOL_MODES]}
                        onChange={(v) => onChange({ ...modes, [n]: v })}
                      />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      <div className="row con-add-tool">
        <input
          id={ids.add}
          className="input mono"
          aria-label="Add a tool by name"
          placeholder="tool_name"
          value={name}
          maxLength={128}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== 'Enter') return
            e.preventDefault()
            add()
          }}
        />
        <Button icon={Plus} disabled={!name.trim()} onClick={add}>
          Add tool
        </Button>
      </div>
    </div>
  )
}

/** Reads the tool list from the connector's server again, signed in as the admin. */
function RefreshTools({ id, onFresh }: { id: string; onFresh: (c: AdminConnector) => void }) {
  const refresh = useRefreshConnectorTools()
  return (
    <div className="con-refresh">
      <div className="row con-refresh-row">
        <Button size="sm" icon={RefreshCw} disabled={refresh.isPending} onClick={() => refresh.mutate(id, { onSuccess: onFresh })}>
          {refresh.isPending ? 'Refreshing tools' : 'Refresh tools'}
        </Button>
        <span className="hint">
          {refresh.isSuccess ? 'Read just now. New tools are listed below; save to keep the modes.' : 'Reads the list again from the server, signed in as you. Connect your own account first.'}
        </span>
      </div>
      {refresh.isError && <span className="hint err">{problemText(refresh.error)}</span>}
    </div>
  )
}

// A connector's slug, as the server takes it (connectors.ValidSlug).
const SLUG = /^[a-z][a-z0-9]{1,30}$/

/** The redirect address to show: the saved connector's own, or the prefix and the slug being typed. */
function redirectOf(existing: AdminConnector | undefined, prefix: string | undefined, slug: string): { uri?: string; needsSlug?: boolean } {
  if (existing) return { uri: existing.redirectUri ?? '' }
  if (prefix === undefined || !prefix) return { uri: prefix }
  return SLUG.test(slug) ? { uri: prefix + slug } : { uri: '', needsSlug: true }
}

const FORM_FIELDS = new Set(['name', 'slug', 'url', 'oauth.clientId', 'oauth.clientSecret', 'scopes', 'allowedGroups'])

function ConnectorForm({
  start,
  existing,
  entry,
  redirectPrefix,
  onDone,
  onConflict,
}: {
  start: Draft
  existing?: AdminConnector
  entry?: CatalogEntry | null
  /** Each connector's redirect address is this followed by its slug. */
  redirectPrefix?: string
  onDone: () => void
  /** A save found the connector changed: the page says so, and the form is made again from the fresh copy. */
  onConflict: (msg: string) => void
}) {
  const create = useCreateConnector()
  const update = useUpdateConnector()
  const remove = useDeleteConnector()
  const [asking, setAsking] = useState(false)
  const save = existing ? update : create
  const ids = { name: useId(), slug: useId(), url: useId(), id: useId(), secret: useId(), scopes: useId(), groups: useId() }
  const [d, setD] = useState(start)
  const [secret, setSecret] = useState('')
  // The connector as a tool refresh answered it: newer tools and rev than existing.
  const [fresh, setFresh] = useState<AdminConnector>()
  const cur = fresh ?? existing
  const reload = useReloadConnectors()
  const done = () => {
    // The list still has the rev from before the refresh.
    if (fresh) reload()
    onDone()
  }
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((x) => ({ ...x, [k]: v }))
  const errs = save.isError && !(existing && isConflict(save.error)) ? fieldErrors(save.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const other = [...errs.other, ...Object.entries(errs.fields).filter(([f]) => !FORM_FIELDS.has(f)).map(([, m]) => m)]
  const title = existing ? existing.name : entry ? `Add ${entry.name}` : 'Add a custom connector'

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (cur)
      update.mutate(
        { id: cur.id, body: inputOf(d, secret, cur.rev) },
        { onSuccess: done, onError: (err) => isConflict(err) && onConflict(problemText(err)) },
      )
    else create.mutate(inputOf(d, secret), { onSuccess: onDone })
  }

  const err = (f: string) => errs.fields[f] && <span className="hint err">{errs.fields[f]}</span>

  return (
    <Card className="pad tok-form">
      <form onSubmit={submit} noValidate aria-label={title}>
        <h3 className="card-h">{title}</h3>
        {entry?.setup && (
          <p className="sub con-setup">
            {entry.setup}
            {entry.docs && (
              <>
                {' '}
                <a href={entry.docs} target="_blank" rel="noopener noreferrer">
                  The vendor's guide <ExternalLink size={13} aria-hidden="true" />
                </a>
              </>
            )}
          </p>
        )}
        {other.length > 0 && (
          <div className="field">
            <Banner tone="err" title={other.join(' ')} />
          </div>
        )}
        <div className="field">
          <label htmlFor={ids.name}>Name</label>
          <input id={ids.name} className="input" value={d.name} maxLength={100} autoComplete="off" onChange={(e) => set('name', e.target.value)} />
          <span className="hint">What people see, such as Slack.</span>
          {err('name')}
        </div>
        <div className="field">
          <label htmlFor={ids.slug}>Short name</label>
          <input
            id={ids.slug}
            className="input mono"
            value={d.slug}
            maxLength={31}
            autoComplete="off"
            spellCheck={false}
            readOnly={!!existing}
            onChange={(e) => set('slug', e.target.value)}
          />
          <span className="hint">
            {existing
              ? 'Fixed once added. Agents see its tools as this name, then two underscores, then the tool.'
              : '2 to 31 lowercase letters and digits, such as slack. Agents see its tools as slack__post_message. It cannot change later.'}
          </span>
          {err('slug')}
        </div>
        <div className="field">
          <label htmlFor={ids.url}>Address of the MCP server</label>
          <input
            id={ids.url}
            className="input mono"
            value={d.url}
            placeholder="https://mcp.example.org/mcp"
            maxLength={2048}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => set('url', e.target.value)}
          />
          {existing && <span className="hint">Changing it disconnects everyone who connected.</span>}
          {err('url')}
        </div>
        <div className="field">
          <span className="field-label">Redirect address</span>
          <RedirectAddress {...redirectOf(existing, redirectPrefix, d.slug)} />
          <span className="hint">Paste this at the vendor as the OAuth redirect (callback) address.</span>
          {cur?.oauth.issuer && <span className="hint">Sign-ins go to {cur.oauth.issuer}, found at the first sign-in.</span>}
          {cur?.oauth.serverMoved && (
            <span className="hint con-status warn">
              The server now names another sign-in server. Saving it trusts the new sign-in server, disconnects everyone and needs the client
              secret again.
            </span>
          )}
        </div>
        <div className="field">
          <label htmlFor={ids.id}>OAuth client id</label>
          <input id={ids.id} className="input mono" value={d.clientId} maxLength={512} autoComplete="off" spellCheck={false} onChange={(e) => set('clientId', e.target.value)} />
          <span className="hint">From the OAuth app you made at the vendor. Leave empty when the vendor lets Tapas register itself.</span>
          {err('oauth.clientId')}
        </div>
        <div className="field">
          <label htmlFor={ids.secret}>OAuth client secret</label>
          <input
            id={ids.secret}
            className="input mono"
            type="password"
            value={secret}
            maxLength={4096}
            autoComplete="off"
            onChange={(e) => setSecret(e.target.value)}
          />
          <span className="hint">
            {cur?.oauth.secretStored
              ? 'A secret is stored, encrypted. Leave this empty to keep it, or paste a new one to replace it.'
              : 'Stored encrypted and never shown again.'}
          </span>
          {err('oauth.clientSecret')}
        </div>
        <div className="field">
          <label htmlFor={ids.scopes}>Scopes</label>
          <input id={ids.scopes} className="input mono" value={d.scopes} autoComplete="off" spellCheck={false} onChange={(e) => set('scopes', e.target.value)} />
          <span className="hint">What Tapas asks the vendor for, separated by spaces.</span>
          {err('scopes')}
        </div>
        <div className="field">
          <label htmlFor={ids.groups}>Allowed groups</label>
          <input id={ids.groups} className="input mono" value={d.groups} autoComplete="off" spellCheck={false} onChange={(e) => set('groups', e.target.value)} />
          <span className="hint">
            Members of these groups may use it, separated by commas. * means every member. Empty means nobody, admins included.
          </span>
          {err('allowedGroups')}
        </div>
        <ToolModes
          tools={cur?.tools}
          modes={d.modes}
          onChange={(m) => set('modes', m)}
          refresh={
            cur && (
              <RefreshTools
                id={cur.id}
                onFresh={(c) => {
                  setFresh(c)
                  setD((x) => withTools(x, c.tools))
                }}
              />
            )
          }
        />
        <div className="field fit">
          <Switch label={d.enabled ? 'On' : 'Off'} name="Turned on" checked={d.enabled} onChange={(on) => set('enabled', on)} />
          <span className="hint">Off, no agent sees its tools and nobody can connect to it.</span>
        </div>
        <div className="row tok-form-act con-act">
          {existing && (
            <Button variant="ghost" icon={Trash2} className="con-remove" onClick={() => setAsking(true)}>
              Remove connector
            </Button>
          )}
          <Button variant="ghost" onClick={done}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {existing ? 'Save' : 'Add connector'}
          </Button>
        </div>
      </form>
      {existing && asking && (
        <Confirm
          title={`Remove ${existing.name}?`}
          body="Everyone's connection to it is removed and agents lose its tools at once. This cannot be undone."
          confirm="Remove"
          icon={Trash2}
          danger
          busy={remove.isPending}
          error={remove.isError ? remove.error : undefined}
          onConfirm={() => remove.mutate(existing.id, { onSuccess: done })}
          onCancel={() => setAsking(false)}
        />
      )}
    </Card>
  )
}

/** The catalogue to start from, or an empty form for any MCP server. */
/** How many people connected their own account. */
function connectionsOf(c: AdminConnector): { text: string; tone: 'ok' | 'warn' | 'off' } {
  if (c.oauth.serverMoved) {
    return { text: 'Its server now sends sign-ins to another server: nobody can connect until you check and save it', tone: 'warn' }
  }
  const n = c.connections
  return { text: n === 0 ? 'Nobody connected yet' : n === 1 ? '1 person connected' : `${n} people connected`, tone: n > 0 ? 'ok' : 'off' }
}

function ConnectorRow({ c, onEdit, onError }: { c: AdminConnector; onEdit: () => void; onError: (msg: string) => void }) {
  const update = useUpdateConnector()
  const conn = connectionsOf(c)
  const turn = (enabled: boolean) =>
    update.mutate({ id: c.id, body: inputOf({ ...draftOf(c), enabled }, '', c.rev) }, { onError: (err) => onError(problemText(err)) })
  return (
    <tr>
      <th scope="row">
        <div className="atable-app">
          <ConnectorLogo catalogId={c.catalogId} />
          <div className="atable-name">
            <b>{c.name}</b>
            <span className="mono faint small">{c.slug}</span>
            <span className={`small con-status ${conn.tone}`}>{conn.text}</span>
          </div>
        </div>
      </th>
      <td>
        <Switch label={c.enabled ? 'On' : 'Off'} name={`Turn on ${c.name}`} checked={c.enabled} disabled={update.isPending} onChange={turn} />
      </td>
      <td className="col-act">
        <Button size="sm" variant="ghost" icon={Pencil} aria-label={`Edit ${c.name}`} onClick={onEdit}>
          <span className="atable-act-label">Edit</span>
        </Button>
      </td>
    </tr>
  )
}

// A catalogue entry no connector was made from yet: one click opens its form.
function SetupRow({ e, disabled, onSetUp }: { e: CatalogEntry; disabled: boolean; onSetUp: () => void }) {
  return (
    <tr>
      <th scope="row">
        <div className="atable-app">
          <ConnectorLogo catalogId={e.id} />
          <div className="atable-name">
            <b>{e.name}</b>
          </div>
        </div>
      </th>
      <td className="faint">Not set up</td>
      <td className="col-act">
        <Button size="sm" icon={Plus} aria-label={`Set up ${e.name}`} disabled={disabled} onClick={onSetUp}>
          <span className="atable-act-label">Set up</span>
        </Button>
      </td>
    </tr>
  )
}

type Open = { kind: 'new'; entry: CatalogEntry | null } | { kind: 'edit'; id: string } | null

/**
 * Settings, Connectors (platform admins): the MCP servers agents may reach
 * through Tapas (each person connects their own account), and which of their
 * tools only read.
 */
export function AdminConnectors() {
  const admin = useIsAdmin()
  const q = useConnectors(admin)
  const cat = useConnectorCatalog(admin)
  const [open, setOpenState] = useState<Open>(null)
  // A save that failed outside the form: a change someone else made first, or a switch in the list.
  const [notice, setNotice] = useState('')
  const setOpen = (o: Open) => {
    setOpenState(o)
    setNotice('')
  }
  if (!admin) return null
  if (q.isError) return <Banner tone="err" title={problemText(q.error)} />
  if (!q.data) return <div className="muted">Loading</div>
  const list = q.data
  const redirectPrefix = cat.data?.redirectUriPrefix
  const editing = open?.kind === 'edit' ? list.find((c) => c.id === open.id) : undefined
  const close = () => setOpen(null)
  const rows = listRows(list, cat.data?.items ?? [])
  return (
    <section className="section" aria-label="Connectors">
      <div className="section-head tok-head">
        <p className="sub tok-intro">
          Connectors let agents use other services, such as Slack or GitHub, through Tapas. Agents never see a password or token. Tools that write
          become plans their owner approves.
        </p>
      </div>
      {notice && (
        <div className="section-sm">
          <Banner tone="err" title={notice} />
        </div>
      )}
      {cat.isError && (
        <div className="section-sm">
          <Banner tone="err" title={problemText(cat.error)} />
        </div>
      )}
      {open?.kind === 'new' && (
        <div className="section-sm">
          <ConnectorForm start={draftFrom(open.entry)} entry={open.entry} redirectPrefix={redirectPrefix} onDone={close} onConflict={setNotice} />
        </div>
      )}
      {editing && (
        <div className="section-sm">
          <ConnectorForm
            key={editing.rev}
            start={withTools(draftOf(editing), editing.tools)}
            existing={editing}
            redirectPrefix={redirectPrefix}
            onDone={close}
            onConflict={setNotice}
          />
        </div>
      )}
      <div className="section-sm card atable-wrap">
        <table className="atable">
          <caption className="sr-only">Connectors</caption>
          <thead>
            <tr>
              <th scope="col">Connector</th>
              <th scope="col">Status</th>
              <th scope="col" className="col-act">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) =>
              r.kind === 'connector' ? (
                <ConnectorRow key={r.c.id} c={r.c} onEdit={() => setOpen({ kind: 'edit', id: r.c.id })} onError={setNotice} />
              ) : (
                <SetupRow key={`catalog-${r.e.id}`} e={r.e} disabled={open !== null} onSetUp={() => setOpen({ kind: 'new', entry: r.e })} />
              ),
            )}
          </tbody>
        </table>
      </div>
      <div className="section-sm row">
        <Button icon={Plus} disabled={open !== null} onClick={() => setOpen({ kind: 'new', entry: null })}>
          Add a custom MCP server
        </Button>
      </div>
    </section>
  )
}

type ListRow = { kind: 'connector'; c: AdminConnector } | { kind: 'catalog'; e: CatalogEntry }

// Every catalogue entry in catalogue order, as the connectors made from it or,
// when there are none, as an entry to set up; then the custom connectors.
function listRows(list: AdminConnector[], catalog: CatalogEntry[]): ListRow[] {
  const ids = new Set(catalog.map((e) => e.id))
  const rows: ListRow[] = []
  for (const e of catalog) {
    const made = list.filter((c) => c.catalogId === e.id)
    if (made.length === 0) rows.push({ kind: 'catalog', e })
    for (const c of made) rows.push({ kind: 'connector', c })
  }
  for (const c of list) if (!ids.has(c.catalogId)) rows.push({ kind: 'connector', c })
  return rows
}
