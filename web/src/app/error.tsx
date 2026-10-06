"use client";
import { Button } from "@/components/ui/button";
export default function ErrorPage({reset}:{error:Error;reset:()=>void}){return <main className="min-h-screen grid place-items-center p-6"><div className="text-center"><h1 className="text-xl mb-5">页面暂时无法加载</h1><Button onClick={reset}>重新加载</Button></div></main>}
