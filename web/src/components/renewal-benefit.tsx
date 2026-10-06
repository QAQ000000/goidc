import { money } from "@/lib/api";

export type RenewalBenefitTerms = {renewal_discount_percent:number;renewal_fixed_discount_cents:number;renewal_discount_limit:number;renewal_promo_code:string};
export function renewalBenefitLabel(terms:RenewalBenefitTerms){return terms.renewal_fixed_discount_cents>0?`每件每期减免 ${money(terms.renewal_fixed_discount_cents)}`:`续费减免 ${terms.renewal_discount_percent}%`;}
export function RenewalBenefitDetails({terms,used}:{terms:RenewalBenefitTerms;used?:number}){
 if(!terms.renewal_promo_code)return null;
 const exhausted=used!==undefined&&terms.renewal_discount_limit>0&&used>=terms.renewal_discount_limit;
 return <p className="text-xs text-muted-foreground mt-2 break-words">{terms.renewal_promo_code} · {renewalBenefitLabel(terms)} · {terms.renewal_discount_limit>0?`优惠续费 ${terms.renewal_discount_limit} 次${used!==undefined?`，已使用 ${used} 次`:""}，退款不恢复次数`:"不限续费次数"}{exhausted?" · 已用完，下一期按原价续费":""}</p>;
}
