import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import type { User } from "./api";
export async function requireUser(admin=false):Promise<User>{
 const cookieName=admin ? "zjmfgo_admin_session" : "zjmfgo_session";
 const portal=admin ? "admin" : "customer";
 const cookie= (await cookies()).get(cookieName);
 if(!cookie)redirect(admin ? "/admin-login" : "/login");
 const response=await fetch(`${process.env.API_INTERNAL_URL??"http://127.0.0.1:8088"}/api/v1/me`,{headers:{Cookie:`${cookieName}=${cookie.value}`,"X-Auth-Portal":portal},cache:"no-store"});
 if(response.status===401)redirect(admin ? "/admin-login" : "/login");
 if(!response.ok)throw new Error("账户服务暂时不可用");
 const body=await response.json();const user=body.data as User;
 if(admin&&user.role!=="admin"&&user.role!=="super_admin")redirect("/admin-login");
 if(!admin&&user.role!=="customer")redirect("/login");
 return user;
}
