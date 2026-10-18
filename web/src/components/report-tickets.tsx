"use client";
import { useEffect, useState } from "react";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { ReportPanel } from "./report-shared";
import { ResourceTable, ResourcePagination } from "./resource-table";
import { useAdminPermission } from "./workspace";

type Handler = { handler_id: number; handler_name: string; ticket_count: number; open_count: number; closed_count: number; reply_count: number; rated_reply_count: number; total_score: number; one_star_count: number; two_star_count: number; three_star_count: number; four_star_count: number; five_star_count: number };
type Page = { handlers: { handler_id: number; handler_name: string }[]; items: Handler[]; total: number; limit: number; offset: number; from: string; to: string };
type Filters = { handler: string; from: string; to: string; sort: string; direction: string; offset: number };
const sortLabels = { ticket_count:"创建工单数",open_count:"当前待处理",closed_count:"当前已关闭",reply_count:"公开管理员内容数",rated_reply_count:"已评分内容数",total_score:"总评分",one_star_count:"1星次数",two_star_count:"2星次数",three_star_count:"3星次数",four_star_count:"4星次数",five_star_count:"5星次数",handler_name:"处理人" };
function readTicketFilters(): Filters {
  const params = new URLSearchParams(typeof window === "undefined" ? "" : window.location.search);
  const rawHandler=params.get("ticket_handler") ?? "";
  const handler=/^\d+$/.test(rawHandler)&&Number.isSafeInteger(Number(rawHandler)) ? rawHandler : "";
  const date=(value:string)=>/^\d{4}-\d{2}-\d{2}$/.test(value)&&!Number.isNaN(Date.parse(value))&&new Date(value).toISOString().slice(0,10)===value;
  const from=params.get("ticket_from") ?? "",to=params.get("ticket_to") ?? "";
  const range=date(from)&&date(to)&&from<=to&&Date.parse(to)-Date.parse(from)<=366*86400000;
  const sort=params.get("ticket_sort") ?? "ticket_count",offset=Number(params.get("ticket_offset") ?? 0);
  return {handler,from:range?from:"",to:range?to:"",sort:Object.hasOwn(sortLabels,sort)?sort:"ticket_count",direction:params.get("ticket_direction")==="asc"?"asc":"desc",offset:Number.isSafeInteger(offset)&&offset>=0?offset:0};
}
export function TicketHandlerReport({ year, month, filtersChanged = false }: { year: number; month: number; filtersChanged?: boolean }) {
  const permitted = useAdminPermission("tickets.view");
  const [filter,setFilter] = useState<Filters>(readTicketFilters);
  const [draft,setDraft] = useState(()=>{ const value=readTicketFilters(); return {from:value.from,to:value.to}; });
  const [revision,setRevision] = useState(0);
  useEffect(()=>{const restore=()=>{const next=readTicketFilters();setFilter(next);setDraft({from:next.from,to:next.to});};window.addEventListener("popstate",restore);return()=>window.removeEventListener("popstate",restore);},[]);
  function apply(next: Filters) {
    const params=new URLSearchParams(window.location.search);
    for(const [key,value] of Object.entries({ticket_handler:next.handler,ticket_from:next.from,ticket_to:next.to,ticket_sort:next.sort,ticket_direction:next.direction,ticket_offset:next.offset?String(next.offset):""})) {if(value)params.set(key,value);else params.delete(key);}
    window.history.pushState(null,"",`${window.location.pathname}?${params}`);
    setFilter(next);setDraft({from:next.from,to:next.to});setRevision(value=>value+1);
  }
  const {handler,sort,direction,offset}=filter;
  if (!permitted) return <p role="status">没有查看工单统计的权限。</p>;
  const params = new URLSearchParams({ year: String(year), month: String(month), sort, direction, limit: "25", offset: String(offset) });
  if (filter.handler !== "") params.set("assignee_id", filter.handler);
  if (filter.from && filter.to) { params.set("from", filter.from); params.set("to", filter.to); }
  const path = `/admin/reports/tickets/handlers?${params}`;
  const changed = draft.from !== filter.from || draft.to !== filter.to;
  const valid = (!draft.from && !draft.to || !!draft.from && !!draft.to && draft.from <= draft.to);
  return <div className="mt-6">
    <form className="flex flex-wrap gap-3 items-end mb-4" onSubmit={event => { event.preventDefault(); if (valid) apply({...filter,...draft,offset:0}); }}>

      <label className="text-sm">开始日期<Input type="date" aria-label="工单统计开始日期" value={draft.from} onChange={event => setDraft({...draft,from:event.target.value})} /></label>
      <label className="text-sm">结束日期<Input type="date" aria-label="工单统计结束日期" value={draft.to} onChange={event => setDraft({...draft,to:event.target.value})} /></label>
      <Button disabled={!valid}>查询处理人统计</Button>
      <Button type="button" variant="outline" onClick={() => { apply({handler:"",from:"",to:"",sort:"ticket_count",direction:"desc",offset:0}); }}>重置筛选</Button>
    </form>
    <div className="mb-4 flex flex-wrap gap-3"><label className="text-sm">排序 <select aria-label="工单统计排序字段" className="border rounded p-2" value={sort} onChange={event => apply({...filter,sort:event.target.value,offset:0})}>{Object.entries(sortLabels).map(([value,label])=><option value={value} key={value}>{label}</option>)}</select></label><label className="text-sm">方向 <select aria-label="工单统计排序方向" className="border rounded p-2" value={direction} onChange={event=>apply({...filter,direction:event.target.value,offset:0})}><option value="desc">降序</option><option value="asc">升序</option></select></label></div>
    <ReportPanel<Page> key={`${path}:${revision}`} path={path} title="处理人和评分统计" description="工单按创建期间、当前处理人归属；评分按公开回复或管理员代建初始内容的创建期间、原作者归属。创建量和已关闭量保留合并来源；已关闭仅指当前closed（不含resolved）。按当前部门范围限制。" filtersChanged={changed || filtersChanged}>
      {data => <><label className="text-sm block mb-4">处理人 <select aria-label="工单统计处理人" className="border rounded p-2" value={handler} onChange={event => apply({...filter,handler:event.target.value,offset:0})}><option value="">全部授权范围内处理人</option>{data.handlers.map(option=><option value={option.handler_id} key={option.handler_id}>{option.handler_name}（{option.handler_id}）</option>)}</select></label><p className="text-sm text-muted-foreground mb-4">期间：{data.from} 至 {data.to}；无明确历史身份的旧回复不计入管理员内容，未评分内容不计为零星。</p><ResourceTable items={data.items} rowKey={item=>item.handler_id} empty="此期间暂无授权范围内的数据" columns={[
        {header:"处理人",render:item=>`${item.handler_name}（${item.handler_id}）`},
        {header:"创建工单",render:item=>item.ticket_count},{header:"当前待处理",render:item=>item.open_count},{header:"当前已关闭",render:item=>item.closed_count},
        {header:"公开管理员内容",render:item=>item.reply_count},{header:"已评分内容",render:item=>item.rated_reply_count},
        {header:"总评分",render:item=>item.total_score},
        {header:"1星",render:item=>item.one_star_count},{header:"2星",render:item=>item.two_star_count},{header:"3星",render:item=>item.three_star_count},{header:"4星",render:item=>item.four_star_count},{header:"5星",render:item=>item.five_star_count},
      ]}/><ResourcePagination total={data.total} offset={offset} limit={25} unit="处理人" onOffsetChange={offset=>apply({...filter,offset})}/></>}
    </ReportPanel>
  </div>;
}
