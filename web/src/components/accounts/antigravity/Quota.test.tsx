import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { AntigravityQuota } from './Quota'

describe('Antigravity quota', () => {
  it('displays model-specific remaining quota and reset times', () => {
    render(<AntigravityQuota account={{ id: 'example-account', provider: 'antigravity', display_name: 'Example Google account', status: 'active', quota_snapshot: { models: {
      'example-flash': { displayName: 'Example Flash', quotaInfo: { remainingFraction: 0.75, resetTime: '2030-01-01T00:00:00Z' } },
      'example-pro': { displayName: 'Example Pro', quotaInfo: { remainingFraction: 0 } },
    } } }} />)
    expect(screen.getByText('Example Flash · 75% remaining')).toBeInTheDocument()
    expect(screen.getByText('Example Pro · 0% remaining')).toBeInTheDocument()
    expect(screen.getByText('Reset time not reported')).toBeInTheDocument()
    expect(screen.getByLabelText('Antigravity model quotas')).toBeInTheDocument()
  })

  it('explains missing quota instead of displaying a fabricated balance', () => {
    render(<AntigravityQuota account={{ id: 'example-account', provider: 'antigravity', display_name: 'Example Google account', status: 'active' }} />)
    expect(screen.getByText('Model quota unavailable')).toBeInTheDocument()
    expect(screen.queryByText(/0% remaining/)).not.toBeInTheDocument()
  })
})
