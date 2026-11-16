import { api } from './api'
export async function arrivalFor(taskIds: string[]) {
  if (!navigator.geolocation) throw new Error('此浏览器不支持定位，请使用支持定位的 HTTPS 页面')
  const location = await new Promise<GeolocationPosition>((resolve, reject) => navigator.geolocation.getCurrentPosition(resolve, () => reject(new Error('定位失败：请授权位置访问，并到达配送点后重试')), { enableHighAccuracy: true, maximumAge: 0, timeout: 15000 }))
  return api<{arrivalToken:string;expiresAt:string}>('/courier/arrivals', { method: 'POST', body: { taskIds, latitude: location.coords.latitude, longitude: location.coords.longitude, accuracy: location.coords.accuracy, observedAt: new Date(location.timestamp).toISOString() } })
}
