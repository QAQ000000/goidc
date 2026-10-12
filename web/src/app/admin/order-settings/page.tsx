import {OrderSettingsView} from "@/components/order-settings";
import {requireUser} from "@/lib/session";
export default async function Page(){await requireUser(true);return <OrderSettingsView/>}
