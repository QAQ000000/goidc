import { notFound } from "next/navigation";
import { InvoiceApplicationDetailView } from "@/components/invoice-applications";
export default async function Page({params}:{params:Promise<{id:string}>}){const {id}=await params;const value=Number(id);if(!Number.isSafeInteger(value)||value<1)notFound();return <InvoiceApplicationDetailView id={value}/>;}
