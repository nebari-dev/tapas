import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import catalogJSON from '../../../internal/connectors/catalog/catalog.json?raw'
import { ConnectorLogo } from './KindLogo'

// The catalogue ids as the gateway ships them.
const CATALOG_IDS: string[] = (JSON.parse(catalogJSON) as { entries: { id: string }[] }).entries.map((e) => e.id)

describe('ConnectorLogo', () => {
  it('reads the catalogue the gateway ships', () => {
    expect(CATALOG_IDS).toContain('slack')
    expect(CATALOG_IDS.length).toBeGreaterThanOrEqual(7)
  })

  it.each(CATALOG_IDS)('draws the vendor logo for the %s catalogue entry', (id) => {
    const { container } = render(<ConnectorLogo catalogId={id} />)
    expect(container.querySelector('svg.lucide')).toBeNull()
    expect(container.querySelector('.mark')).not.toBeNull()
  })

  it.each(['', 'not-in-the-catalogue'])('draws a plug line icon for a custom connector (%j)', (id) => {
    const { container } = render(<ConnectorLogo catalogId={id} />)
    expect(container.querySelector('svg.lucide-plug')).not.toBeNull()
  })
})
