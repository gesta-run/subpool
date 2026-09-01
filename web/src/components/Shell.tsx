import { useEffect, useState, type ComponentType, type SVGProps } from 'react'
import {
  AccountIcon,
  CloseIcon,
  KeyIcon,
  LogoutIcon,
  MenuIcon,
  PoolIcon,
  SettingsIcon,
  UsageIcon,
} from './Icons'

export type PageID = 'accounts' | 'pools' | 'api-keys' | 'usage' | 'settings'

interface NavigationItem {
  id: PageID
  label: string
  description: string
  icon: ComponentType<SVGProps<SVGSVGElement>>
}

export const navigation: NavigationItem[] = [
  { id: 'accounts', label: 'Accounts', description: 'Codex subscriptions', icon: AccountIcon },
  { id: 'pools', label: 'Pools', description: 'Routing groups', icon: PoolIcon },
  { id: 'api-keys', label: 'API Keys', description: 'Employee access', icon: KeyIcon },
  { id: 'usage', label: 'Usage', description: 'Token totals', icon: UsageIcon },
  { id: 'settings', label: 'Settings', description: 'Global routing rules', icon: SettingsIcon },
]

interface ShellProps {
  activePage: PageID
  onNavigate: (page: PageID) => void
  onLogout: () => void
  children: React.ReactNode
}

export function Shell({ activePage, onNavigate, onLogout, children }: ShellProps) {
  const [mobileOpen, setMobileOpen] = useState(false)
  const active = navigation.find((item) => item.id === activePage) ?? navigation[0]

  useEffect(() => {
    setMobileOpen(false)
  }, [activePage])

  return (
    <div className="app-shell">
      {mobileOpen ? <button className="mobile-scrim" type="button" aria-label="Close navigation" onClick={() => setMobileOpen(false)} /> : null}
      <aside className={`sidebar ${mobileOpen ? 'sidebar--open' : ''}`} aria-label="Primary navigation">
        <div className="brand">
          <span className="brand__mark" aria-hidden="true"><i /><i /><i /></span>
          <div><strong>SUBPOOL</strong><span>CONTROL PLANE</span></div>
        </div>
        <button className="sidebar__close icon-button" type="button" aria-label="Close navigation" onClick={() => setMobileOpen(false)}>
          <CloseIcon />
        </button>
        <nav className="nav-list">
          {navigation.map((item, index) => {
            const Icon = item.icon
            return (
              <a
                key={item.id}
                className={`nav-item ${activePage === item.id ? 'nav-item--active' : ''}`}
                href={`#/${item.id}`}
                aria-current={activePage === item.id ? 'page' : undefined}
                onClick={() => onNavigate(item.id)}
              >
                <span className="nav-item__index">0{index + 1}</span>
                <Icon className="nav-item__icon" />
                <span><strong>{item.label}</strong><small>{item.description}</small></span>
              </a>
            )
          })}
        </nav>
        <div className="sidebar__footer">
          <span className="status-dot" />
          <span><strong>Gateway online</strong><small>Single instance</small></span>
        </div>
      </aside>
      <div className="shell-content">
        <header className="topbar">
          <button className="menu-button icon-button" type="button" aria-label="Open navigation" onClick={() => setMobileOpen(true)}>
            <MenuIcon />
          </button>
          <div className="topbar__title">
            <span>Administration / {active.label}</span>
            <h1>{active.label}</h1>
          </div>
          <button className="logout-button button button--ghost" type="button" onClick={onLogout}>
            <LogoutIcon className="button__icon" />
            <span>Sign out</span>
          </button>
        </header>
        <main className="work-area">{children}</main>
      </div>
    </div>
  )
}
