import { Server, Cloud, Database, Globe, Box } from "lucide-react";
export const serviceIcons = { server:Server, cloud:Cloud, database:Database, globe:Globe, box:Box };
export const serviceIconLabels = { server:"服务器", cloud:"云服务", database:"数据库", globe:"网络", box:"其他" };
export function serviceIcon(name:string){return serviceIcons[name as keyof typeof serviceIcons]??Server;}
