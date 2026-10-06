import { ServicesView } from "@/components/data-pages";
import { requireUser } from "@/lib/session";
import { notFound } from "next/navigation";
export default async function Page({params}:{params:Promise<{id:string}>}){
 await requireUser();const {id}=await params;
 if(!/^\d+$/.test(id)||!Number.isSafeInteger(Number(id)))notFound();
 return <ServicesView key={id} groupId={Number(id)}/>;
}
