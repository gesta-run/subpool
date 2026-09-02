import { useCallback, useEffect, useState } from 'react'
import { APIError, errorMessage, request } from './api'
import { AppSkeleton } from './components/PageSkeleton'
import { Shell, type PageID } from './components/Shell'
import { AccountsPage } from './pages/AccountsPage'
import { APIKeysPage } from './pages/APIKeysPage'
import { LoginPage } from './pages/LoginPage'
import { OverviewPage } from './pages/OverviewPage'
import { PoolsPage } from './pages/PoolsPage'
import { UsagePage } from './pages/UsagePage'
import { SettingsPage } from './pages/SettingsPage'

type AuthState = 'checking' | 'authenticated' | 'anonymous'

function pageFromHash(): PageID {
  const value = window.location.hash.replace(/^#\//, '')
  return ['overview', 'accounts', 'pools', 'api-keys', 'usage', 'settings'].includes(value) ? value as PageID : 'accounts'
}

function CurrentPage({ page }: { page: PageID }) {
  if (page === 'overview') return <OverviewPage />
  if (page === 'accounts') return <AccountsPage />
  if (page === 'pools') return <PoolsPage />
  if (page === 'api-keys') return <APIKeysPage />
  if (page === 'usage') return <UsagePage />
  return <SettingsPage />
}

export default function App() {
  const [auth, setAuth] = useState<AuthState>('checking')
  const [connectionError, setConnectionError] = useState('')
  const [page, setPage] = useState<PageID>(pageFromHash)

  const checkSession = useCallback(async () => {
    setAuth('checking')
    setConnectionError('')
    try {
      await request('/api/v1/auth/session')
      setAuth('authenticated')
    } catch (error) {
      if (error instanceof APIError && error.status === 401) {
        setAuth('anonymous')
      } else {
        setConnectionError(errorMessage(error))
        setAuth('anonymous')
      }
    }
  }, [])

  useEffect(() => { void checkSession() }, [checkSession])
  useEffect(() => {
    const onUnauthorized = () => setAuth('anonymous')
    window.addEventListener('subpool:unauthorized', onUnauthorized)
    return () => window.removeEventListener('subpool:unauthorized', onUnauthorized)
  }, [])
  useEffect(() => {
    const onHashChange = () => setPage(pageFromHash())
    window.addEventListener('hashchange', onHashChange)
    return () => window.removeEventListener('hashchange', onHashChange)
  }, [])

  async function logout() {
    try { await request('/api/v1/auth/logout', { method: 'POST' }) } catch { /* Clear local state even if the session expired. */ } finally { setAuth('anonymous') }
  }

  if (auth === 'checking') {
    return <AppSkeleton />
  }

  if (auth === 'anonymous') {
    return <LoginPage onSuccess={() => setAuth('authenticated')} connectionError={connectionError} onRetryConnection={() => void checkSession()} />
  }

  return (
    <Shell activePage={page} onNavigate={setPage} onLogout={() => void logout()}>
      <CurrentPage page={page} />
    </Shell>
  )
}
