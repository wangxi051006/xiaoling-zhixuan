import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, API_BASE, getToken, shortTime } from './api'
import { mergeNotifications, parseSSE } from './notifications.js'
export const ORDER_REFRESH_EVENT = 'campus-order-refresh'
type Message = { id: string; orderId: string; title: string; body: string; createdAt: string; readAt?: string }
export function NotificationCenter({ owner }: { owner: string }) {
  const [items, setItems] = useState<Message[]>([]), [unread, setUnread] = useState(0), [open, setOpen] = useState(false), [toast, setToast] = useState<Message|null>(null), [error,setError]=useState('')
  const readsInFlight=useRef(new Set<string>())
  useEffect(() => {
    let generation = 0
    let alive = true, cursor = '0', initial = true, delay = 1000, syncing: Promise<void>|null = null
    let reconnect: ReturnType<typeof setTimeout>|undefined, toastTimer: ReturnType<typeof setTimeout>|undefined
    let controller = new AbortController()
    setItems([]); setUnread(0); setToast(null)
    const sync = () => {
      if (syncing) return syncing
      syncing = (async () => {
        let page: any
        let incoming: Message[] = []
        do {
          page = await api<any>('/notifications?afterId=' + encodeURIComponent(cursor) + '&limit=50', {signal:controller.signal})
          if (!alive) return
          const messages: Message[] = page.items || []
          incoming = mergeNotifications(incoming, messages)
          if (messages.length) cursor = String(page.nextCursor || messages[messages.length - 1].id)
          setItems(previous => mergeNotifications(previous, messages))
          setUnread(page.unreadCount || 0)
          if (!messages.length || page.hasMore === false || (page.hasMore === undefined && messages.length < 50)) break
        } while (alive)
        if (!alive) return
        setItems(previous => mergeNotifications(previous, incoming)); setError('')
        if (!initial && incoming.length) {
          const unreadItems = incoming.filter(item=>!item.readAt)
          const latest = unreadItems[unreadItems.length-1]
          if (latest) { setToast(latest); clearTimeout(toastTimer); toastTimer=setTimeout(()=>setToast(null),8000) }
          window.dispatchEvent(new Event(ORDER_REFRESH_EVENT))
        }
        initial = false
      })().finally(() => { syncing = null })
      return syncing
    }
    const connect = async () => {
      if (!alive || document.hidden) return
      const connectionGeneration = generation
      const connectionController = controller
      try {
        await sync()
        const response = await fetch(API_BASE + '/notifications/stream?afterId=' + encodeURIComponent(cursor), { headers: { Accept: 'text/event-stream', Authorization: 'Bearer ' + getToken() }, signal: connectionController.signal })
        if (!response.ok || !response.body) throw new Error('消息连接暂不可用，正在重连')
        delay=1000
        const reader = response.body.getReader(), decoder = new TextDecoder()
        let buffer = ''
        while (alive) {
          const chunk = await reader.read()
          if (chunk.done) break
          const parsed = parseSSE(buffer + decoder.decode(chunk.value, {stream:true})); buffer=parsed.rest
          for (const event of parsed.events) if (event.event === 'delivery_notification') await sync()
        }
      } catch(err:any) { if(alive && err.name!=='AbortError') setError(err.message || '消息暂时离线，重连后会补取') }
      finally { if(alive && !document.hidden && connectionGeneration === generation) { reconnect=setTimeout(connect,delay); delay=Math.min(delay*2,30000) } }
    }
    const visible = () => {
      clearTimeout(reconnect)
      generation++
      if (document.hidden) controller.abort()
      else { controller=new AbortController(); void connect() }
    }
    document.addEventListener('visibilitychange', visible)
    void connect()
    return () => { alive=false; controller.abort(); clearTimeout(reconnect); clearTimeout(toastTimer); document.removeEventListener('visibilitychange',visible) }
  }, [owner])
  async function markRead(item: Message) {
    if (item.readAt || readsInFlight.current.has(item.id)) return
    readsInFlight.current.add(item.id)
    try { await api('/notifications/' + item.id + '/read', {method:'POST'}); setItems(previous=>previous.map(row=>row.id===item.id ? {...row,readAt:new Date().toISOString()} : row)); setUnread(count=>Math.max(0,count-1)) } catch(err:any) {setError(err.message)} finally {readsInFlight.current.delete(item.id)}
  }
  return <div className="notification-center">
    <button className="btn" onClick={()=>setOpen(!open)}>消息 {unread > 0 && <b className="badge badge-danger">{unread} 条未读</b>}</button>
    {toast && <div className="delivery-toast" role="status"><b>{toast.title}</b><p>{toast.body}</p><Link to={'/user/orders/'+toast.orderId} onClick={()=>{void markRead(toast);setToast(null)}}>查看订单</Link><button aria-label="关闭提醒" onClick={()=>setToast(null)}>×</button></div>}
    {open && <section className="panel notification-list"><div className="section-head"><h3>站内消息</h3><button className="btn btn-ghost" onClick={()=>setOpen(false)}>关闭</button></div>{error && <p className="muted" role="status">{error}</p>}{!items.length && <p className="muted">暂无消息</p>}{[...items].reverse().map(item=><article className={'message-row '+(!item.readAt?'unread':'')} key={item.id}><b>{item.title}</b><p>{item.body}</p><small>{shortTime(item.createdAt)}</small><div className="card-actions"><Link to={'/user/orders/'+item.orderId} onClick={()=>void markRead(item)}>查看订单</Link>{!item.readAt && <button className="btn btn-ghost" onClick={()=>void markRead(item)}>标为已读</button>}</div></article>)}</section>}
  </div>
}




