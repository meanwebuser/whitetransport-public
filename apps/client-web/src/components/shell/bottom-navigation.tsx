import { Home, ListChecks, Settings } from 'lucide-react'

export type ShellTab = 'home' | 'endpoints' | 'settings'

interface BottomNavigationProps {
  readonly activeTab: ShellTab
  readonly onChange: (tab: ShellTab) => void
}

const navigationItems: readonly { readonly tab: ShellTab; readonly label: string; readonly testId: string; readonly icon: typeof Home }[] = [
  { tab: 'home', label: 'Главная', testId: 'nav-home', icon: Home },
  { tab: 'endpoints', label: 'Endpoints', testId: 'nav-endpoints', icon: ListChecks },
  { tab: 'settings', label: 'Настройки', testId: 'nav-settings', icon: Settings },
]

/** Fixed, keyboard-accessible navigation shared by the Wails and Capacitor hosts. */
export function BottomNavigation({ activeTab, onChange }: BottomNavigationProps) {
  return (
    <nav className="shell-bottom-nav" aria-label="Основная навигация">
      <div className="shell-bottom-nav__inner">
        {navigationItems.map(({ tab, label, testId, icon: Icon }) => (
          <button
            key={tab}
            type="button"
            className={`shell-nav-item${activeTab === tab ? ' shell-nav-item--active' : ''}`}
            aria-current={activeTab === tab ? 'page' : undefined}
            data-testid={testId}
            onClick={() => onChange(tab)}
          >
            <Icon aria-hidden="true" size={21} strokeWidth={activeTab === tab ? 2.5 : 2} />
            <span>{label}</span>
          </button>
        ))}
      </div>
    </nav>
  )
}
