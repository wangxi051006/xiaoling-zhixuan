import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, getToken, setToken } from './api'
import { TOKEN_KEY, SESSION_KEY, SESSION_EXPIRED_EVENT } from './session.js'
import type { Session, SessionUser } from './types'

type AuthValue = {
  session: Session | null
  login: (phone: string, password: string) => Promise<Session>
  logout: () => void
  isAdmin: boolean
  isCourier: boolean
  sessionExpired: boolean
}

const AuthContext = createContext<AuthValue | null>(null)

function loadSession(): Session | null {
  try {
    const raw = localStorage.getItem(SESSION_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Session
    if (!parsed?.accessToken || !parsed?.user || parsed.accessToken !== getToken()) return null
    return parsed
  } catch {
    return null
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(loadSession)
  const [sessionExpired, setSessionExpired] = useState(false)

  useEffect(() => {
    function expired() { setSession(null); setSessionExpired(true) }
    function syncSession(event: StorageEvent) {
      if (event.key === null || event.key === SESSION_KEY || event.key === TOKEN_KEY) {
        setSession(loadSession())
      }
    }
    window.addEventListener(SESSION_EXPIRED_EVENT, expired)
    window.addEventListener('storage', syncSession)
    return () => {
      window.removeEventListener(SESSION_EXPIRED_EVENT, expired)
      window.removeEventListener('storage', syncSession)
    }
  }, [])

  async function login(phone: string, password: string) {
    const result = await api<Session>('/auth/login', { method: 'POST', body: { phone, password } })
    localStorage.setItem(SESSION_KEY, JSON.stringify(result))
    setToken(result.accessToken)
    setSession(result)
    setSessionExpired(false)
    return result
  }

  function logout() {
    api('/auth/logout', { method: 'POST' }).catch(() => undefined)
    localStorage.removeItem(SESSION_KEY)
    localStorage.removeItem('campus_token')
    setSession(null)
    setSessionExpired(false)
  }

  const value = useMemo<AuthValue>(() => {
    const roles = session?.user.roles || []
    return {
      session,
      sessionExpired,
      login,
      logout,
      isAdmin: roles.some((role) => ['store_admin', 'system_admin', 'catalog_operator', 'warehouse_operator', 'dispatcher', 'support', 'finance'].includes(role)),
      isCourier: roles.includes('courier'),
    }
  }, [session, sessionExpired])

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthValue {
  const value = useContext(AuthContext)
  if (!value) throw new Error('AuthProvider missing')
  return value
}

export function roleLabel(role: string): string {
  const labels: Record<string, string> = {
    store_admin: '店长',
    system_admin: '系统管理员',
    catalog_operator: '商品运营',
    warehouse_operator: '仓储',
    dispatcher: '调度',
    courier: '配送员',
    support: '客服',
    finance: '财务',
  }
  return labels[role] || role
}

export function userHome(user: SessionUser): string {
  if (user.roles?.includes('courier')) return '/delivery/tasks'
  if (user.roles?.some((role) => ['store_admin', 'system_admin', 'catalog_operator', 'warehouse_operator', 'dispatcher', 'support', 'finance'].includes(role))) {
    return '/admin/workbench'
  }
  return '/user'
}
