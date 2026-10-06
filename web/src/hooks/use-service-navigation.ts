"use client";
import { useEffect } from "react";
import { useResource } from "./use-resource";
import type { ServiceNavigationGroup } from "@/lib/api";
export function refreshServiceNavigation(){window.dispatchEvent(new Event("zjmfgo:service-navigation"));}
export function useServiceNavigation(enabled=true){
 const resource=useResource<ServiceNavigationGroup[]>(enabled?"/service-navigation":null,false);
 const refresh=resource.refresh;
 useEffect(()=>{if(!enabled)return;window.addEventListener("zjmfgo:service-navigation",refresh);return ()=>window.removeEventListener("zjmfgo:service-navigation",refresh);},[enabled,refresh]);
 return resource;
}
