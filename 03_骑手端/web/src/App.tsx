import { DeliveryPoints } from './pages/DeliveryPoints'
import { useState, type FormEvent, type ReactNode } from 'react'
import { Navigate, Outlet, Route, Routes, useNavigate } from 'react-router-dom'
import { api } from './api'
import { useAuth, userHome } from './auth'
import { Shell } from './components'
import { AdminOrderDetail, AdminOrders, AdminProducts, AdminRefunds, AdminStaff, AdminWorkbench, Reports } from './pages/Admin'
import { DeliveryTaskDetail, DeliveryTasks } from './pages/Delivery'
import { UserHome, UserOrderDetail, UserOrders } from './pages/User'

function Login() {
  const { session, login, sessionExpired } = useAuth()
  const navigate = useNavigate()
  const demoMode = import.meta.env.VITE_DEMO_MODE === 'true' || (import.meta.env.DEV && import.meta.env.VITE_DEMO_MODE !== 'false')
  const [phone, setPhone] = useState(demoMode ? '13800000001' : '')
  const [password, setPassword] = useState(demoMode ? 'CampusDemo!2026' : '')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  if (session) return <Navigate to={userHome(session.user)} replace />
  async function submit(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError('')
    try {
      const result = await login(phone.trim(), password)
      navigate(userHome(result.user), { replace: true })
    } catch (err: any) {
      setError(err.message || '登录失败')
    } finally {
      setBusy(false)
    }
  }
  const demos = [
    ['店长 / 管理端', '13800000001'],
    ['配送员', '13800000004'],
    ['顾客', '13900000001'],
  ]
  return (
    <div className="login-page">
      <div className="login-art">
        <div className="brand big"><span className="brand-mark">校</span><b>校零智选</b></div>
        <h1>校园里的<br /><em>刚刚好</em>。</h1>
        <p>真实库存、服务端报价、付款确认后才履约。<br />管理端 / 用户 H5 / 配送 H5 使用同一套 Go + MySQL 业务服务。</p>
        <div className="art-chips"><span>暖黄校园</span><span>批次效期</span><span>首单快照</span><span>真实聚合</span></div>
      </div>
      <div className="login-card">
        <h2>登录</h2>
        {sessionExpired && <p className="notice" role="status">登录状态已失效，请重新登录原账号继续查看订单。</p>}
        <p className="muted">{demoMode ? <>本地种子账号，密码来自 <code>DEMO_PASSWORD</code>。</> : '使用已开通的手机号与密码登录。'}</p>
        <form onSubmit={submit}>
          <label>手机号<input value={phone} onChange={(e) => setPhone(e.target.value)} autoComplete="username" /></label>
          <label>密码<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" /></label>
          {error && <div className="form-error">{error}</div>}
          <button className="btn btn-primary btn-block" disabled={busy}>{busy ? '登录中…' : '进入校零智选'}</button>
        </form>
        {demoMode && (
          <div className="demo-logins">
            {demos.map(([label, value]) => (
              <button key={value} type="button" className="chip" onClick={() => { setPhone(value); setPassword('CampusDemo!2026') }}>{label}</button>
            ))}
          </div>
        )}
        <p className="fine">真实环境不使用默认账号；种子只在 <code>APP_ENV=local</code> 且显式开启时创建。</p>
      </div>
    </div>
  )
}

function RequireAuth({ children }: { children: ReactNode }) {
  const { session } = useAuth()
  if (!session) return <Navigate to="/login" replace />
  return <>{children}</>
}

function RequireAdmin({ children }: { children: ReactNode }) {
  const { isAdmin } = useAuth()
  if (!isAdmin) return <Navigate to="/" replace />
  return <>{children}</>
}

function RequireCourier({ children }: { children: ReactNode }) {
  const { isCourier } = useAuth()
  if (!isCourier) return <Navigate to="/" replace />
  return <>{children}</>
}

function Home() {
  const { session } = useAuth()
  if (!session) return <Navigate to="/login" replace />
  return <Navigate to={userHome(session.user)} replace />
}

function Layout() {
  return <Shell><Outlet /></Shell>
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route element={<RequireAuth><Layout /></RequireAuth>}>
        <Route path="/" element={<Home />} />
        <Route path="/admin/workbench" element={<RequireAdmin><AdminWorkbench /></RequireAdmin>} />
        <Route path="/admin/orders" element={<RequireAdmin><AdminOrders /></RequireAdmin>} />
        <Route path="/admin/orders/:id" element={<RequireAdmin><AdminOrderDetail /></RequireAdmin>} />
        <Route path="/admin/products" element={<RequireAdmin><AdminProducts /></RequireAdmin>} />
        <Route path="/admin/refunds" element={<RequireAdmin><AdminRefunds /></RequireAdmin>} />
        <Route path="/admin/reports" element={<RequireAdmin><Reports /></RequireAdmin>} />
        <Route path="/admin/delivery-points" element={<RequireAdmin><DeliveryPoints /></RequireAdmin>} />
        <Route path="/admin/staff" element={<RequireAdmin><AdminStaff /></RequireAdmin>} />
        <Route path="/user" element={<UserHome />} />
        <Route path="/user/orders" element={<UserOrders />} />
        <Route path="/user/orders/:id" element={<UserOrderDetail />} />
        <Route path="/delivery/tasks" element={<RequireCourier><DeliveryTasks /></RequireCourier>} />
        <Route path="/delivery/tasks/:id" element={<RequireCourier><DeliveryTaskDetail /></RequireCourier>} />
        <Route path="*" element={<div className="state-card">页面不存在</div>} />
      </Route>
    </Routes>
  )
}

