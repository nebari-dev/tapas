import { ArrowLeft } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { Banner } from '../../components/Banner'
import { Card } from '../../components/Card'
import { problemText } from '../../lib/http'
import { useCompleteSignIn } from '../../lib/queries'

/** What the vendor sent the browser back with, read once from the address. */
type Answer = { kind: 'code'; state: string; code: string; iss: string } | { kind: 'refused' } | { kind: 'missing' }

function answerOf(search: string): Answer {
  const q = new URLSearchParams(search)
  if (q.get('error')) return { kind: 'refused' }
  const state = q.get('state') ?? ''
  const code = q.get('code') ?? ''
  if (!state || !code) return { kind: 'missing' }
  return { kind: 'code', state, code, iss: q.get('iss') ?? '' }
}

function Back() {
  return (
    <Link className="btn" to="/settings/connections">
      <ArrowLeft size={16} aria-hidden="true" />
      Back to Connected accounts
    </Link>
  )
}

/**
 * Settings, the page a connector's vendor sends the browser back to after a
 * sign-in. Each connector has its own (its slug ends the path), and the page
 * says which it was reached on, so the server can refuse an answer meant for
 * another connector. It hands what the vendor sent to the server once, and
 * takes the one-time code out of the address bar and the history first.
 */
export function ConnectionCallback() {
  const { pathname, search } = useLocation()
  const { slug = '' } = useParams()
  const navigate = useNavigate()
  const [answer] = useState(() => answerOf(search))
  const complete = useCompleteSignIn()
  // StrictMode runs effects twice; a code can be used only once.
  const sent = useRef(false)
  useEffect(() => {
    if (sent.current) return
    sent.current = true
    navigate(pathname, { replace: true })
    if (answer.kind === 'code') complete.mutate({ slug, state: answer.state, code: answer.code, ...(answer.iss ? { iss: answer.iss } : {}) })
  }, [answer, complete, navigate, pathname, slug])

  let body
  if (answer.kind === 'refused')
    body = <Banner tone="err" title="The service did not connect your account. It may have been cancelled there. Try connecting again." />
  else if (answer.kind === 'missing')
    body = <Banner tone="err" title="This page is missing what the service sends back. Start connecting again from Connected accounts." />
  else if (complete.isError) body = <Banner tone="err" title={problemText(complete.error)} />
  else if (complete.isSuccess)
    body = (
      <Banner tone="ok" title={`${complete.data.name} is connected`}>
        Your agents can now use its tools as you, through Tapas.
      </Banner>
    )
  else body = <div className="muted">Finishing the sign-in</div>

  return (
    <section className="section" aria-label="Connecting an account">
      <Card className="pad con-callback">
        {body}
        {(answer.kind !== 'code' || !complete.isPending) && (
          <div className="row">
            <Back />
          </div>
        )}
      </Card>
    </section>
  )
}
