"use client";
import { canDiscard, useEditorState, type EditorState } from "./file-editor-state";
import Link from "next/link";
import { useRef, useState } from "react";
import { Download,Plus,Save,Trash2,RefreshCw } from "lucide-react";
import { api,ApiError,upload,fetchDownload,date,type CustomerFile,type AdminCustomerFile } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace,useAdminPermission } from "./workspace";
import { fileSize } from "./downloads";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Table,TableHeader,TableHead,TableBody,TableRow,TableCell } from "./ui/table";
import { Dialog,DialogContent,DialogHeader,DialogTitle,DialogDescription } from "./ui/dialog";
import { Loading,Failure } from "./common";
import { AdminPagination } from "./admin-pagination";

type FilePage<T>={items:T[];total:number};
type Owner={id:number;username:string;display_name:string;status:string};
const selectStyle="h-9 w-full min-w-0 rounded-md border bg-white px-3 text-sm";
function Pager({offset,total,change}:{offset:number;total:number;change:(offset:number)=>void}){return <div className="mt-5 flex flex-wrap justify-between gap-3 text-sm"><span>{total} 个文件</span><div className="flex gap-2"><Button variant="outline" disabled={offset===0} onClick={()=>change(Math.max(0,offset-20))}>上一页</Button><Button variant="outline" disabled={offset+20>=total} onClick={()=>change(offset+20)}>下一页</Button></div></div>;}
function FileDescription({file}:{file:CustomerFile}){return <div className="min-w-0 flex-1"><h2 className="font-medium break-words">{file.title}</h2><p className="mt-2 whitespace-pre-wrap text-sm text-muted-foreground break-words">{file.remarks}</p><p className="mt-3 text-xs text-muted-foreground break-words">{file.filename} · {fileSize(file.size_bytes)} · {file.downloads} 次下载</p><p className="mt-1 text-xs text-muted-foreground">更新于 {date(file.updated_at)}</p><details className="mt-2 text-xs text-muted-foreground"><summary className="cursor-pointer">SHA-256 校验值</summary><code className="block mt-2 break-all">{file.sha256}</code></details></div>;}
export function CustomerFilesView(){
 const [search,setSearch]=useState("");const [query,setQuery]=useState("");const [offset,setOffset]=useState(0);const [busy,setBusy]=useState<number|null>(null);const [message,setMessage]=useState("");
 const files=useResource<FilePage<CustomerFile>>(`/customer-files?q=${encodeURIComponent(query)}&offset=${offset}&limit=20`,false);
 async function download(file:CustomerFile){if(busy!==null)return;setBusy(file.id);setMessage("");try{await fetchDownload(file,`/customer-files/${file.id}/file`);files.refresh();}catch(e){setMessage((e as Error).message);}finally{setBusy(null);}}
 return <Workspace title="我的文件"><div className="mb-8"><h1 className="text-2xl font-semibold">我的文件</h1><p className="mt-2 text-muted-foreground text-sm">客服为你提供的专属资料，仅账户本人可访问。<Link href="/downloads" className="ml-2 text-primary">公共下载中心</Link></p></div><form className="flex gap-3 mb-6" onSubmit={e=>{e.preventDefault();setQuery(search);setOffset(0);}}><Input aria-label="搜索我的文件" placeholder="搜索名称、备注或文件名" value={search} onChange={e=>setSearch(e.target.value)}/><Button variant="outline">搜索</Button></form>{message && <p role="alert" className="text-sm text-destructive mb-5">{message}</p>}{files.error ? files.error instanceof ApiError && files.error.code==="downloads_disabled" ? <div className="rounded-lg border p-8 text-center">文件下载暂时关闭</div> : <Failure error={files.error} retry={files.refresh}/> : !files.data ? <Loading/> : <><div className="divide-y border-y">{files.data.items.map(file=><article key={file.id} className="py-6 flex flex-wrap items-start gap-4"><FileDescription file={file}/><Button disabled={busy!==null} aria-label={`下载 ${file.title}`} onClick={()=>void download(file)}><Download size={16}/>{busy===file.id ? "下载中…" : "下载文件"}</Button></article>)}{files.data.items.length===0 && <p className="py-16 text-center text-muted-foreground">暂无专属文件</p>}</div><Pager offset={offset} total={files.data.total} change={setOffset}/></>}</Workspace>;
}
export function AdminCustomerFilesView({customerId}:{customerId?:number}){return <Workspace title="客户文件" admin><CustomerFileManager customerId={customerId}/></Workspace>;}
export function CustomerFileManager({ customerId }: { customerId?: number }) {
  const [editor, setEditor] = useState<EditorState>({ busy: false, dirty: false });
  const manage = useAdminPermission("customer_files.manage");
  const [customer, setCustomer] = useState(customerId ? String(customerId) : "");
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [offset, setOffset] = useState(0);
  const [selection, setSelection] = useState<AdminCustomerFile[]>([]);
  const [edit, setEdit] = useState<AdminCustomerFile | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const root = "/admin/customer-files";
  const files = useResource<FilePage<AdminCustomerFile>>(
    `${root}?q=${encodeURIComponent(query)}&customer_id=${customer}&limit=20&offset=${offset}`,
    false
  );

  function refresh() {
    files.refresh();
    setSelection([]);
  }

  function newFile() {
    setEdit({
      id: 0,
      customer_id: Number(customer) || 0,
      title: "",
      remarks: "",
      filename: "",
      mime_type: "",
      size_bytes: 0,
      sha256: "",
      downloads: 0,
      created_at: "",
      updated_at: "",
      version: 0,
      uploader_id: 0,
      uploader_name: "",
      customer_name: "",
      customer_username: "",
    });
  }

  async function open(id: number) {
    if (busy || editor.busy) return;
    setBusy(true);
    setError("");
    try {
      setEdit(await api<AdminCustomerFile>(`${root}/${id}`));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function download(file: AdminCustomerFile) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await fetchDownload(file, `${root}/${file.id}/file`);
      refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function bulkDelete() {
    if (!busy && selection.length && window.confirm(`确认删除选中的 ${selection.length} 个客户文件？`)) {
      setBusy(true);
      setError("");
      try {
        await api(`${root}/bulk-delete`, {
          method: "POST",
          body: JSON.stringify({ items: selection.map((f) => ({ id: f.id, expected_version: f.version })) }),
        });
        refresh();
      } catch (e) {
        setError((e as Error).message);
      } finally {
        setBusy(false);
      }
    }
  }

  function changePage(value: number) {
    setOffset(value);
    setSelection([]);
  }

  return (
    <div className="bg-white rounded border border-[#ebeef5] shadow-sm p-4 sm:p-5 space-y-4 max-w-full overflow-hidden">
      {/* Top guide text matching MoFang */}
      <div className="text-xs text-[#606266]">
        为授权客户上传、维护和转移专属资料，仅所属客户可访问。{" "}
        <a
          href="https://bbs.idcsmart.com"
          target="_blank"
          rel="noreferrer"
          className="text-[#409eff] hover:underline ml-1"
        >
          帮助文档
        </a>
      </div>

      {/* Action Toolbar */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          {manage && (
            <Button
              disabled={busy || editor.busy}
              onClick={newFile}
              size="sm"
              className="h-8 bg-[#1cb24d] hover:bg-[#169941] text-white text-xs px-3.5 rounded shadow-none font-normal"
            >
              <Plus size={14} className="mr-1" />
              上传客户文件
            </Button>
          )}
          {manage && selection.length > 0 && (
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => void bulkDelete()}
              className="h-8 text-xs text-[#f56c6c] border-[#fde2e2] bg-[#fef0f0] hover:bg-[#fde2e2]"
            >
              <Trash2 size={13} className="mr-1" />
              批量删除 ({selection.length})
            </Button>
          )}
        </div>

        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            setQuery(search);
            setOffset(0);
            setSelection([]);
          }}
        >
          <Input
            aria-label="筛选客户编号"
            type="number"
            min={1}
            value={customer}
            placeholder="客户编号"
            onChange={(e) => {
              setCustomer(e.target.value);
              setOffset(0);
              setSelection([]);
            }}
            className="h-8 w-28 text-xs border-[#dcdfe6]"
          />
          <Input
            aria-label="搜索客户文件"
            value={search}
            placeholder="搜索名称或备注"
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 w-44 text-xs border-[#dcdfe6]"
          />
          <Button
            type="submit"
            size="sm"
            className="h-8 bg-[#409eff] text-white hover:bg-[#337ecc] text-xs px-3 shadow-none font-normal"
          >
            搜索
          </Button>
          <Button
            type="button"
            variant="outline"
            size="icon"
            className="h-8 w-8 text-[#606266] border-[#dcdfe6]"
            title="刷新"
            aria-label="刷新"
            onClick={refresh}
          >
            <RefreshCw size={13} />
          </Button>
        </form>
      </div>

      {error && <p role="alert" className="p-2.5 bg-[#fef0f0] border border-[#fde2e2] text-xs text-[#f56c6c] rounded">{error}</p>}

      {/* Data Table */}
      {files.error ? (
        <Failure error={files.error} retry={refresh} />
      ) : !files.data ? (
        <Loading />
      ) : (
        <>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader className="bg-[#f5f7fa]">
                <TableRow className="border-b border-[#ebeef5] hover:bg-[#f5f7fa]">
                  {manage && <TableHead className="w-10 text-center text-xs font-semibold text-[#909399] py-3">选择</TableHead>}
                  <TableHead className="text-xs font-semibold text-[#909399] py-3">文件名称</TableHead>
                  <TableHead className="text-xs font-semibold text-[#909399] py-3">所属客户</TableHead>
                  <TableHead className="text-xs font-semibold text-[#909399] py-3">上传人 / 版本</TableHead>
                  <TableHead className="text-xs font-semibold text-[#909399] py-3">下载次数</TableHead>
                  <TableHead className="text-right text-xs font-semibold text-[#909399] py-3 pr-4">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {files.data.items.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={manage ? 6 : 5} className="h-32 text-center text-xs text-[#909399]">
                      暂无客户文件
                    </TableCell>
                  </TableRow>
                ) : (
                  files.data.items.map((file) => (
                    <TableRow key={`${file.id}:${file.version}`} className="border-b border-[#ebeef5] hover:bg-[#f5f7fa] text-xs text-[#606266]">
                      {manage && (
                        <TableCell className="text-center">
                          <Checkbox
                            aria-label={`选择 ${file.title}`}
                            checked={selection.some((f) => f.id === file.id && f.version === file.version)}
                            onCheckedChange={(value) =>
                              setSelection((current) =>
                                value
                                  ? [...current.filter((f) => f.id !== file.id), file]
                                  : current.filter((f) => f.id !== file.id)
                              )
                            }
                          />
                        </TableCell>
                      )}
                      <TableCell className="min-w-52">
                        <div className="font-medium text-[#303133]">{file.title}</div>
                        <div className="text-[11px] text-[#909399] mt-0.5">
                          {file.filename} · {fileSize(file.size_bytes)}
                        </div>
                        {file.remarks && <div className="text-[11px] text-[#909399] italic mt-0.5">{file.remarks}</div>}
                      </TableCell>
                      <TableCell>
                        <Link href={`/admin/customers/${file.customer_id}`} className="text-[#409eff] hover:underline font-medium">
                          #{file.customer_id} {file.customer_name}
                        </Link>
                        <div className="text-[11px] text-[#909399]">{file.customer_username}</div>
                      </TableCell>
                      <TableCell>
                        <span className="text-[#606266]">{file.uploader_name}</span>
                        <span className="text-[11px] text-[#909399] block">v{file.version}</span>
                      </TableCell>
                      <TableCell>
                        <span className="tabular-nums font-semibold text-[#303133]">{file.downloads}</span>
                        <span className="text-[#909399] text-[11px] ml-1">次</span>
                      </TableCell>
                      <TableCell className="text-right pr-4">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={busy}
                            className="h-7 text-xs border-[#dcdfe6] text-[#409eff] hover:border-[#409eff] hover:bg-[#ecf5ff]"
                            aria-label={`下载客户文件 ${file.title}`}
                            onClick={() => void download(file)}
                          >
                            <Download size={12} className="mr-1" />
                            下载
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={busy}
                            className="h-7 text-xs border-[#dcdfe6] text-[#606266] hover:border-[#409eff] hover:text-[#409eff]"
                            aria-label={`查看客户文件 ${file.title}`}
                            onClick={() => void open(file.id)}
                          >
                            {manage ? "编辑" : "查看"}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>

          <AdminPagination
            total={files.data.total}
            page={Math.floor(offset / 20)}
            pageSize={20}
            onPageChange={(p) => changePage(p * 20)}
          />
        </>
      )}

      <Dialog
        open={!!edit}
        onOpenChange={(value) => {
          if (!value && canDiscard(editor)) setEdit(null);
        }}
      >
        <DialogContent className="sm:max-w-xl max-h-[90dvh] overflow-y-auto bg-white border border-[#ebeef5] rounded shadow-lg p-0 overflow-hidden">
          <DialogHeader className="px-5 py-4 border-b border-[#ebeef5] bg-[#fafafa]">
            <DialogTitle className="text-sm font-semibold text-[#303133]">
              {edit?.id ? (manage ? "编辑客户文件" : "查看客户文件") : "上传客户文件"}
            </DialogTitle>
            <DialogDescription className="text-xs text-[#909399]">
              仅所属客户可读取。转移文件后原客户将立即失去访问权限。
            </DialogDescription>
          </DialogHeader>
          {edit && (
            <div className="p-5">
              <CustomerFileForm
                report={setEditor}
                key={`${edit.id}:${edit.version}`}
                initial={edit}
                manage={manage}
                saved={refresh}
                close={() => setEdit(null)}
                reload={() => void open(edit.id)}
              />
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
function CustomerFileForm({initial,manage,saved,close,reload,report}:{report:(state:EditorState)=>void;initial:AdminCustomerFile;manage:boolean;saved:()=>void;close:()=>void;reload:()=>void}){
 const fileInput=useRef<HTMLInputElement>(null);
 const [form,setForm]=useState(initial);const [file,setFile]=useState<File|null>(null);const [busy,setBusy]=useState(false);const [error,setError]=useState("");const [conflict,setConflict]=useState(false);const [ownerQuery,setOwnerQuery]=useState("");const [ownerSearch,setOwnerSearch]=useState("");
 const owners=useResource<Owner[]>(manage ? `/admin/customer-files/owners?q=${encodeURIComponent(ownerQuery)}` : null,false);
 const ownerOptions=owners.data ?? [];const initialOwner:Owner={id:initial.customer_id,display_name:initial.customer_name,username:initial.customer_username,status:"active"};
 const [selectedOwner,setSelectedOwner]=useState(initialOwner);
 const dirty=manage&&(JSON.stringify(form)!==JSON.stringify(initial)||file!==null);useEditorState(busy,dirty,report);
 async function save(e:React.FormEvent){e.preventDefault();if(!manage || busy)return;setBusy(true);setError("");setConflict(false);try{
  if(file && file.size>50*1024*1024)throw new Error("文件不能超过 50 MiB");const body=new FormData();body.set("metadata",JSON.stringify({customer_id:form.customer_id,title:form.title,remarks:form.remarks,expected_version:form.version}));if(file)body.set("file",file);
  await upload(`/admin/customer-files${form.id ? `/${form.id}` : ""}`,body,form.id ? "PUT" : "POST");saved();close();
 }catch(e){setError((e as Error).message);setConflict(e instanceof ApiError && e.code==="version_conflict");}finally{setBusy(false);}}
 async function remove(){if(!manage || busy || !window.confirm("确认删除此客户文件？"))return;setBusy(true);try{await api(`/admin/customer-files/${form.id}`,{method:"DELETE",body:JSON.stringify({expected_version:form.version})});saved();close();}catch(e){setError((e as Error).message);setConflict(e instanceof ApiError && e.code==="version_conflict");}finally{setBusy(false);}}
 return <form onSubmit={e=>void save(e)} className="space-y-4"><fieldset disabled={!manage || busy} className="space-y-4"><h3 className="border-b pb-3 text-sm font-semibold">客户与文件信息</h3>{manage && <div><label className="text-sm block">查找客户<div className="flex gap-2 mt-2"><Input value={ownerSearch} placeholder="搜索客户名称、账号或编号" onChange={e=>setOwnerSearch(e.target.value)}/><Button type="button" variant="outline" onClick={()=>setOwnerQuery(ownerSearch)}>查找</Button></div></label>{owners.error && <Failure error={owners.error} retry={owners.refresh}/>}</div>}<label className="text-sm block">所属客户<select className={`${selectStyle} mt-2`} required value={form.customer_id || ""} onChange={e=>{const id=Number(e.target.value);setForm({...form,customer_id:id});setSelectedOwner(ownerOptions.find(o=>o.id===id) ?? initialOwner);}}><option value="">请选择客户</option>{selectedOwner.id>0 && !ownerOptions.some(o=>o.id===selectedOwner.id) && <option value={selectedOwner.id}>#{selectedOwner.id} {selectedOwner.display_name || "当前客户"}</option>}{ownerOptions.map(o=><option key={o.id} value={o.id}>#{o.id} {o.display_name}（{o.username}）{o.status!=="active" && "（停用）"}</option>)}</select></label><label className="text-sm block">文件名称<Input className="mt-2" required maxLength={255} value={form.title} onChange={e=>setForm({...form,title:e.target.value})}/></label><label className="text-sm block">客户可见备注<Textarea className="mt-2" maxLength={10000} value={form.remarks} onChange={e=>setForm({...form,remarks:e.target.value})}/></label><div className="border-t pt-4"><h3 className="mb-3 text-sm font-semibold">文件上传</h3>{initial.filename && <p className="text-sm mb-2 break-words">当前文件：{initial.filename} · {fileSize(initial.size_bytes)}</p>}<label className="text-sm block">{initial.filename ? "替换文件（留空保留原文件）" : "上传文件"}<Input className="mt-2" ref={fileInput} type="file" required={!initial.filename} accept=".txt,.vcf,.png,.jpg,.jpeg,.gif,.pdf,.zip,.rar,.doc,.docx,.xls,.xlsx,.ppt,.pptx,.rtf,.key,.numbers,.pages" onChange={e=>setFile(e.target.files?.[0] ?? null)}/></label><p className="text-xs text-muted-foreground mt-2">支持文档、图片和压缩包，最大 50 MiB。</p>{initial.sha256 && <code className="block text-xs text-muted-foreground break-all mt-2">SHA-256：{initial.sha256}</code>}</div></fieldset>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}{conflict && <Button type="button" variant="outline" onClick={()=>{if(canDiscard({busy,dirty}))reload();}}><RefreshCw size={16}/>重新加载</Button>}<div className="flex flex-wrap justify-end gap-2 border-t pt-4">{manage && form.id>0 && <Button type="button" disabled={busy} variant="destructive" onClick={()=>void remove()}><Trash2 size={16}/>删除文件</Button>}{manage&&<Button type="button" disabled={busy} variant="outline" onClick={()=>{setForm(initial);setSelectedOwner(initialOwner);setFile(null);if(fileInput.current)fileInput.current.value="";setOwnerQuery("");setOwnerSearch("");setError("");setConflict(false);}}>取消更改</Button>}<Button type="button" disabled={busy} variant="ghost" onClick={()=>{if(canDiscard({busy,dirty}))close();}}>关闭</Button>{manage && <Button disabled={busy}><Save size={16}/>{busy ? "保存中…" : "保存客户文件"}</Button>}</div></form>;
}
