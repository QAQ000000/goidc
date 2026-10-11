import { ServiceTrafficView } from "@/components/service-traffic";
import { notFound } from "next/navigation";
export default async function Page({params}:{params:Promise<{id:string}>}){const {id}=await params;const value=Number(id);if(!Number.isSafeInteger(value)||value<1)notFound();return <ServiceTrafficView id={value}/>;}
