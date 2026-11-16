const api=require('./api')
let owner='',cursor='0',initial=true,items=[],unreadCount=0,active=false,running=false,timer,failures=0
const listeners=new Set()
function resetOwner(){const user=wx.getStorageSync('campus_user');const next=api.token()&&user&&!api.isStaff(user)&&!api.isCourier(user)?String(user.id):'';if(owner!==next){owner=next;cursor='0';initial=true;items=[];unreadCount=0}return owner}
function state(){resetOwner();return {notifications:items.slice().reverse(),notificationUnread:unreadCount}}
function emit(){for(const listener of listeners)listener(state())}
async function poll(){
 if(running||!active)return
 clearTimeout(timer);running=true
 try{
  const user=wx.getStorageSync('campus_user')
  if(!api.token()||!user||api.isStaff(user)||api.isCourier(user))return
  resetOwner();const requestOwner=owner
  let incoming=[],page
  do{page=await api.request('/notifications?afterId='+encodeURIComponent(cursor)+'&limit=50');if(resetOwner()!==requestOwner)return;const rows=page.items||[];incoming.push(...rows);if(rows.length)cursor=String(page.nextCursor||rows[rows.length-1].id);unreadCount=page.unreadCount||0;const pageMap=new Map(items.map(item=>[String(item.id),item]));for(const item of rows)pageMap.set(String(item.id),item);items=[...pageMap.values()];emit();if(!rows.length||page.hasMore===false||(page.hasMore===undefined&&rows.length<50))break}while(active)
  const byId=new Map(items.map(item=>[String(item.id),item]));for(const item of incoming)byId.set(String(item.id),item);items=[...byId.values()];emit()
  const pages=getCurrentPages(),current=pages[pages.length-1]
  if(!initial&&incoming.length){
   const latest=incoming.filter(item=>!item.readAt).pop()
   if(latest&&current?.route!=='pages/user/checkout/checkout')wx.showToast({title:latest.title+'：'+latest.body,icon:'none',duration:4000})
   if(current&&['pages/user/orders/orders','pages/user/order-detail/order-detail'].includes(current.route)&&current.load)current.load()
  }
  initial=false;failures=0
 }catch(error){failures++}
 finally{running=false;if(active)timer=setTimeout(poll,Math.min(10000*2**failures,60000))}
}
function start(){active=true;poll()}
function stop(){active=false;clearTimeout(timer)}
function subscribe(listener){listeners.add(listener);listener(state());return()=>listeners.delete(listener)}
async function read(id){await api.request('/notifications/'+id+'/read','POST',{});const item=items.find(row=>String(row.id)===String(id));if(item&&!item.readAt){item.readAt=new Date().toISOString();unreadCount=Math.max(0,unreadCount-1)}emit()}
module.exports={start,stop,poll,subscribe,read}


