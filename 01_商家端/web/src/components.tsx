import { type ReactNode, useEffect, useRef, useState } from 'react'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { money } from './api'
import { NotificationCenter, ORDER_REFRESH_EVENT } from './NotificationCenter'
import { roleLabel, useAuth, userHome } from './auth'

export function Loading({ text = '加载中…' }: { text?: string }) {
  return <div className="state-card"><span className="spinner" />{text}</div>
}

export function ErrorBox({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="state-card state-error">
      <div>{message}</div>
      {onRetry && <button className="btn btn-ghost" onClick={onRetry}>重试</button>}
    </div>
  )
}

export function Empty({ text = '暂无数据' }: { text?: string }) {
  return <div className="state-card muted">{text}</div>
}

export function Money({ cents }: { cents: number }) {
  return <span className="money">{money(cents)}</span>
}

export function Badge({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'good' | 'warn' | 'danger' | 'info' }) {
  return <span className={`badge badge-${tone}`}>{children}</span>
}

export function StatusBadge({ status }: { status: string }) {
  const map: Record<string, { label: string; tone: 'neutral' | 'good' | 'warn' | 'danger' | 'info' }> = {
    PENDING_PAYMENT: { label: '待支付', tone: 'warn' },
    PAID: { label: '已付款', tone: 'info' },
    PICKING: { label: '备货中', tone: 'warn' },
    READY: { label: '待出餐', tone: 'good' },
    DELIVERING: { label: '配送中', tone: 'info' },
    COMPLETED: { label: '已完成', tone: 'good' },
    CANCELLED: { label: '已取消', tone: 'neutral' },
    CLOSED: { label: '已关闭', tone: 'neutral' },
    PAYMENT_EXCEPTION: { label: '支付异常', tone: 'danger' },
    UNPAID: { label: '未付款', tone: 'warn' },
    PARTIAL_REFUNDED: { label: '部分退款', tone: 'warn' },
    REFUNDED: { label: '已退款', tone: 'neutral' },
    OPEN: { label: '待领取', tone: 'warn' },
    ACCEPTED: { label: '已接单', tone: 'info' },
    PICKED_UP: { label: '已取货', tone: 'info' },
    EXCEPTION: { label: '异常', tone: 'danger' },
  }
  const item = map[status] || { label: status, tone: 'neutral' as const }
  return <Badge tone={item.tone}>{item.label}</Badge>
}

export function FirstOrderBanner({ count, ordinal }: { count: number; ordinal: number }) {
  return (
    <div className="first-order">
      <strong>新用户 · 首次下单</strong>
      <span>累计下单 {count} 次（含本单） · 本单为第 {ordinal} 次</span>
    </div>
  )
}

export function CustomerCount({ count, ordinal, first }: { count: number; ordinal: number; first: boolean }) {
  return (
    <div className={`customer-count ${first ? 'is-first' : ''}`}>
      <span>累计下单 <b>{count}</b> 次（含本单）</span>
      <span>本单第 <b>{ordinal}</b> 次</span>
    </div>
  )
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: string; actions?: ReactNode }) {
  return (
    <header className="page-head">
      <div>
        <h1>{title}</h1>
        {subtitle && <p>{subtitle}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  )
}

export function Shell({ children }: { children: ReactNode }) {
  const { session, logout, isAdmin, isCourier } = useAuth()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  if (!session) return <>{children}</>
  const user = session.user
  const adminLinks = [
    ['/admin/workbench', '履约工作台'],
    ['/admin/orders', '订单'],
    ['/admin/products', '商品库存'],
    ['/admin/refunds', '售后退款'],
    ['/admin/reports', '经营分析'],
    ['/admin/staff', '员工与审计'],
    ['/admin/delivery-points', '配送点设置'],
  ]
  const userLinks = [
    ['/user', '首页选购'],
    ['/user/orders', '我的订单'],
  ]
  const courierLinks = [
    ['/delivery/tasks', '配送任务'],
  ]
  const links = isAdmin ? adminLinks : isCourier ? courierLinks : userLinks
  return (
    <div className="app-shell">
      <aside className={`side ${open ? 'is-open' : ''}`}>
        <Link to="/" className="brand" onClick={() => setOpen(false)}>
          <span className="brand-mark">校</span>
          <span><b>校零智选</b><small>CAMPUS RETAIL</small></span>
        </Link>
        <nav>
          {links.map(([path, label]) => (
            <NavLink key={path} to={path} className={({ isActive }) => `nav-link ${isActive ? 'active' : ''}`} onClick={() => setOpen(false)}>
              {label}
            </NavLink>
          ))}
        </nav>
        <div className="side-foot">
          <div className="who">
            <span>{user.nickname}</span>
            <small>{user.roles.length ? user.roles.map(roleLabel).join(' · ') : '顾客'}</small>
          </div>
          <button className="btn btn-ghost" onClick={() => { logout(); navigate('/login') }}>退出</button>
        </div>
      </aside>
      <div className="main">
        <header className="mobile-head">
          <button className="menu-btn" onClick={() => setOpen(!open)} aria-label="菜单">☰</button>
          <Link to={userHome(user)} className="brand-small">校零智选</Link>
          <span className="muted small">{user.nickname}</span>
        </header>
        <main className="content">{!isAdmin && !isCourier && <NotificationCenter key={user.id} owner={user.id} />}{children}</main>
      </div>
    </div>
  )
}

export function Notice({ text, tone = 'info', onClose }: { text: string; tone?: 'info' | 'danger' | 'good'; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 4200)
    return () => clearTimeout(timer)
  }, [text])
  return <div className={`notice notice-${tone}`}>{text}<button onClick={onClose}>×</button></div>
}

export function useAsync<T>(loader: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const loaderRef = useRef(loader); loaderRef.current = loader
  const refreshRef = useRef<()=>void>(()=>{})
  useEffect(() => {
    let alive = true, inFlight = false, loaded = false, failures = 0, queued = false
    let timer: ReturnType<typeof setTimeout> | undefined
    setLoading(true); setError('')
    async function run() {
      if (!alive || document.hidden) return
      if (inFlight) { queued = true; return }
      clearTimeout(timer); inFlight = true
      try { const value = await loaderRef.current(); if (alive) { setData(value); setError(''); loaded = true; failures=0 } }
      catch (err:any) { if (alive) { failures++; if (!loaded) setError(err.message || '加载失败') } }
      finally {
        inFlight=false
        if (alive) {
          setLoading(false)
          const interval = queued ? 0 : Math.min(10000 * 2 ** failures, 60000)
          queued=false; timer=setTimeout(run,interval)
        }
      }
    }
    const refresh = () => { if (!document.hidden) void run() }
    refreshRef.current=refresh
    window.addEventListener('focus',refresh)
    window.addEventListener(ORDER_REFRESH_EVENT,refresh)
    document.addEventListener('visibilitychange',refresh)
    void run()
    return () => { alive=false; clearTimeout(timer); window.removeEventListener('focus',refresh); window.removeEventListener(ORDER_REFRESH_EVENT,refresh); document.removeEventListener('visibilitychange',refresh) }
  }, deps)
  return { data, error, loading, reload: () => refreshRef.current() }
}

