const api=require('./api')
function arrivalFor(taskIds){
 return new Promise((resolve,reject)=>wx.getLocation({type:'wgs84',isHighAccuracy:true,highAccuracyExpireTime:10000,success:resolve,fail:()=>reject(new Error('定位失败，请授权位置访问并到达配送点后重试'))}))
  .then(location=>api.request('/courier/arrivals','POST',{taskIds,latitude:location.latitude,longitude:location.longitude,accuracy:location.accuracy,observedAt:new Date().toISOString()}))
}
module.exports={arrivalFor}
