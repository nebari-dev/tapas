import { Trash2 } from 'lucide-react'
import { useRef, useState } from 'react'
import type { MemoryView } from '../../api'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { Confirm } from '../../components/Confirm'
import { EmptyState } from '../../components/EmptyState'
import { problemText } from '../../lib/http'
import { useDeleteMemory, useMemories } from '../../lib/queries'

const HINT = 'Tell an agent "remember that ..." to add one.'

/** "27 Nov 2026". */
function day(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric' }).format(d)
}

function MemoryRow({ m }: { m: MemoryView }) {
  const del = useDeleteMemory(m.id)
  const [asking, setAsking] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  return (
    <li className="tok-row sec-row">
      <div className="tok-main">
        <span>{m.text}</span>
      </div>
      <dl className="tok-facts sec-facts">
        <div>
          <dt>Saved</dt>
          <dd>{day(m.createdAt)}</dd>
        </div>
      </dl>
      <div className="tok-act sec-act row">
        <Button ref={btn} size="sm" variant="ghost" icon={Trash2} aria-label={`Delete the memory: ${m.text}`} onClick={() => setAsking(true)}>
          <span className="tok-act-label">Delete</span>
        </Button>
      </div>
      {asking && (
        <Confirm
          title="Delete this memory?"
          body={`Agents will no longer remember: ${m.text}`}
          confirm="Delete"
          icon={Trash2}
          danger
          busy={del.isPending}
          error={del.isError ? del.error : undefined}
          onConfirm={() => del.mutate()}
          onCancel={() => {
            del.reset()
            setAsking(false)
            btn.current?.focus()
          }}
        />
      )}
    </li>
  )
}

/** Settings, Memories: what the platform's memory service holds about the signed-in person, each of which they can delete. */
export function Memories() {
  const memories = useMemories()
  const list = memories.data ?? []
  return (
    <section className="section" aria-label="Memories">
      <div className="section-head tok-head">
        <p className="sub tok-intro">What your coding agents remember about you. {HINT}</p>
      </div>
      <div className="section-sm">
        {memories.isError ? (
          <Banner tone="err" title={problemText(memories.error)} />
        ) : memories.isPending ? (
          <div className="muted">Loading</div>
        ) : list.length === 0 ? (
          <EmptyState title="Nothing remembered yet." body={HINT} />
        ) : (
          <Card>
            <ul className="tok-list" aria-label="Memories">
              {list.map((m) => (
                <MemoryRow key={m.id} m={m} />
              ))}
            </ul>
          </Card>
        )}
      </div>
    </section>
  )
}
