// Unknown transport, authorization, or ownership responses cannot disprove an earlier commit.
function keepPending(error) {
 return !error || !error.status || error.status >= 500 || [401,403,404].includes(error.status) || ['REQUEST_IN_PROGRESS','IDEMPOTENCY_CONFLICT','COMMAND_IN_PROGRESS'].includes(error.code);
}
function currentOwner(){
 try{
  if(typeof localStorage!=='undefined'){const session=JSON.parse(localStorage.getItem('campus_session')||'null');if(!session?.user?.id)throw Error('missing identity');return String(session.user.id)}
  if(typeof wx!=='undefined'){const user=wx.getStorageSync('campus_user');if(!user?.id)throw Error('missing identity');return String(user.id)}
  return ''; // Explicit storage adapters without a host identity are used by module tests only.
 }catch{throw new Error('身份待恢复：原请求仍保留。请重新登录原账号后用原请求恢复，勿新建。')}
}
function createPendingStore(storage,owner=currentOwner){
 const memory=new Map();
 function bound(identity){
  const scoped=key=>identity?key+':owner:'+encodeURIComponent(identity):key;
  return {
   get(key){const address=scoped(key);if(memory.has(address))return memory.get(address);let value;try{value=storage.get(address);if(identity&&!value){const legacy=storage.get(key);if(legacy)throw new Error('LEGACY_PENDING')}}catch(error){if(error.message==='LEGACY_PENDING')throw new Error('存在未绑定身份的历史待确认请求，原payload已保留。请先核对原账号与订单，不能自动转给当前账号或换键重下。');throw new Error('无法读取待确认请求，请恢复本地存储后重试；未发送新请求。')}if(value?.__owner&&value.__owner!==identity)throw new Error('请求属于其他账号，已阻止跨身份恢复。请登录原账号。');memory.set(address,value||null);return value||null;},
   put(key,value){if(value?.__owner&&value.__owner!==identity)throw new Error('请求属于其他账号，不能改绑；请登录原账号恢复。');const address=scoped(key),saved=identity?{...value,__owner:identity}:value;memory.set(address,saved);try{storage.set(address,saved)}catch{throw new Error('本地存储失败：原请求已保留在当前页面内存，尚未发送。请恢复存储后用原请求重试，勿刷新或重新下单。')}},
   clear(key){const address=scoped(key);memory.set(address,null);try{storage.remove(address);return ''}catch{try{storage.set(address,null);return '本地清理失败，已写入完成标记。'}catch{return '服务器已确认，但本地清理失败；当前页面已解除待确认。重开后如出现旧请求，请用原请求恢复，不要新建。'}}}
  };
 }
 return {get:key=>bound(owner()).get(key),put:(key,value)=>bound(owner()).put(key,value),clear:key=>bound(owner()).clear(key),bound:()=>bound(owner())};
}
async function executeCommand(store,key,draft,send){
 // Pin the owner for the entire in-flight command, including cleanup after logout/account switch.
 store=store.bound?store.bound():store;
 const pending=store.get(key)||draft;
 store.put(key,pending);
 try{const result=await send(pending);return {result,warning:store.clear(key)}}catch(error){if(!keepPending(error)){const warning=store.clear(key);if(warning)error.message+='；'+warning;}throw error;}
}
export {keepPending,createPendingStore,executeCommand};
