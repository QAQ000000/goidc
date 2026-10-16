"use client";
import { useEffect, useState } from "react";
import { useResource } from "@/hooks/use-resource";
import { date } from "@/lib/api";
import { Button } from "./ui/button";
import { Failure, Loading } from "./common";
type Item = { id:number; event_key:string; event_kind:string; reply_id:number|null; channel:string; outcome:string; reason:string; created_at:string; recipient_kind:string; address:string|null; outbox_status?:string|null };
type Page = { items:Item[]; total:number; limit:number; offset:number };
const events:Record<string,string> = {customer_created:"客户创建工单",customer_replied:"客户公开回复",staff_replied:"客服公开回复"};
const reasons:Record<string,string> = {unsubscribed:"收件人未订阅此渠道",unverified_address:"收件地址尚未验证",missing_address:"未设置收件地址",missing_template:"未配置通知模板",inactive_template:"通知模板未启用",missing_channel:"未配置可用发送渠道",duplicate_address:"收件地址重复，已避免重复发送",invalid_sms_payload:"短信内容不符合发送要求"};
const deliveryStates:Record<string,string> = {pending:"等待发送",processing:"发送处理中",sending:"发送处理中",sent:"发送完成",failed:"发送失败",dead:"已停止重试",dead_letter:"已停止重试"};
export function TicketNotifications({ticketID,reloadKey}:{ticketID:number;reloadKey?:number}) {
 const [offset,setOffset]=useState(0);
 const {data,error,refresh}=useResource<Page>(`/tickets/${ticketID}/notifications?limit=20&offset=${offset}`,false);
 useEffect(()=>{refresh();},[reloadKey,refresh]);
 return <section className="data-surface p-4 sm:p-6 mt-6"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">工单通知摘要</h2><Button type="button" variant="outline" size="sm" onClick={refresh}>刷新通知</Button></div><p className="text-sm text-muted-foreground mt-2">仅展示当前权限允许的记录。已排队表示进入发送队列，不表示已送达；收件地址由接口按权限隐藏。</p>{error?<Failure error={error} retry={refresh}/>:!data?<Loading/>:<><ul className="divide-y mt-4">{data.items.map(item=><li key={item.id} className="py-4 space-y-2 text-sm"><div className="flex flex-wrap gap-3"><strong>{events[item.event_kind]??item.event_kind}</strong><span>{item.channel==="email"?"邮件":item.channel==="sms"?"短信":item.channel}</span><span>{item.outcome==="queued"?"已排队（非送达确认）":item.outcome==="skipped"?"已跳过":item.outcome}</span></div>{item.outbox_status&&<p>发送状态：{deliveryStates[item.outbox_status]??item.outbox_status}</p>}<p className="text-xs text-muted-foreground">{date(item.created_at)} · {item.recipient_kind==="contact"?"联系人":item.recipient_kind==="cc"?"显式抄送":"用户"}{item.reply_id!=null&&<> · 回复 #{item.reply_id}</>}</p>{item.reason&&<p className="break-words">原因：{reasons[item.reason]??item.reason}</p>}{item.address&&<p className="break-all text-muted-foreground">收件地址：{item.address}</p>}<details className="text-xs text-muted-foreground"><summary>事件标识</summary><p className="break-all mt-2">{item.event_key}</p></details></li>)}</ul>{data.items.length===0&&<p className="text-sm text-muted-foreground py-6">暂无当前权限可见的通知记录</p>}<div className="flex flex-wrap gap-3 items-center mt-4"><Button type="button" variant="outline" size="sm" disabled={offset===0} onClick={()=>setOffset(Math.max(0,offset-20))}>上一页</Button><span className="text-sm">共 {data.total} 条 · 第 {Math.floor(offset/20)+1} 页</span><Button type="button" variant="outline" size="sm" disabled={offset+data.items.length>=data.total} onClick={()=>setOffset(offset+20)}>下一页</Button></div></>}</section>;
}
