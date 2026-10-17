"use client";

import { useState } from "react";
import { Save } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Checkbox } from "./ui/checkbox";

export type Profile = {
  id: number;
  profile_type: "individual" | "company";
  title: string;
  tax_number: string;
  bank_name: string;
  bank_account: string;
  company_address: string;
  phone: string;
  version: number;
};

export type Address = {
  id: number;
  recipient: string;
  phone: string;
  province: string;
  city: string;
  region: string;
  address: string;
  postcode: string;
  is_default: boolean;
  version: number;
};

export function clean(value: string) {
  return value.trim();
}

export function ProfileEditor({
  profile,
  onDone,
}: {
  profile?: Profile;
  onDone: () => void;
}) {
  const [type, setType] = useState(profile?.profile_type ?? "individual");
  const [title, setTitle] = useState(profile?.title ?? "");
  const [tax, setTax] = useState(profile?.tax_number ?? "");
  const [phone, setPhone] = useState(profile?.phone ?? "");
  const [bankName, setBankName] = useState(profile?.bank_name ?? "");
  const [bankAccount, setBankAccount] = useState(profile?.bank_account ?? "");
  const [companyAddress, setCompanyAddress] = useState(
    profile?.company_address ?? "",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save() {
    setBusy(true);
    setError("");
    try {
      await api(`/invoice-profiles${profile ? `/${profile.id}` : ""}`, {
        method: profile ? "PUT" : "POST",
        body: JSON.stringify({
          profile_type: type,
          title: clean(title),
          tax_number: clean(tax),
          phone: clean(phone),
          bank_name: clean(bankName),
          bank_account: clean(bankAccount),
          company_address: clean(companyAddress),
          expected_version: profile?.version ?? 0,
        }),
      });
      onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="grid sm:grid-cols-2 gap-3 border-b pb-5">
      <div>
        <Label>抬头类型</Label>
        <Select
          value={type}
          onValueChange={(value) => setType(value as "individual" | "company")}
          disabled={busy}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="individual">个人</SelectItem>
            <SelectItem value="company">公司</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div>
        <Label htmlFor="invoice-title">发票抬头</Label>
        <Input
          id="invoice-title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          disabled={busy}
        />
      </div>
      {type === "company" && (
        <div>
          <Label htmlFor="invoice-tax">税号</Label>
          <Input
            id="invoice-tax"
            value={tax}
            onChange={(e) => setTax(e.target.value)}
            disabled={busy}
          />
        </div>
      )}
      <div>
        <Label htmlFor="invoice-phone">联系电话</Label>
        <Input
          id="invoice-phone"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
          disabled={busy}
        />
      </div>
      {type === "company" && (
        <>
          <div>
            <Label htmlFor="invoice-bank">开户行</Label>
            <Input
              id="invoice-bank"
              maxLength={160}
              value={bankName}
              onChange={(e) => setBankName(e.target.value)}
              disabled={busy}
            />
          </div>
          <div>
            <Label htmlFor="invoice-bank-account">银行账号</Label>
            <Input
              id="invoice-bank-account"
              maxLength={120}
              value={bankAccount}
              onChange={(e) => setBankAccount(e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="sm:col-span-2">
            <Label htmlFor="invoice-company-address">公司地址</Label>
            <Input
              id="invoice-company-address"
              maxLength={300}
              value={companyAddress}
              onChange={(e) => setCompanyAddress(e.target.value)}
              disabled={busy}
            />
          </div>
        </>
      )}
      <div className="sm:col-span-2 flex flex-wrap gap-2">
        <Button
          disabled={
            busy || !clean(title) || (type === "company" && !clean(tax))
          }
          onClick={() => void save()}
        >
          <Save />
          保存抬头
        </Button>
        {error && (
          <p role="alert" className="text-sm text-destructive self-center">
            {error}
          </p>
        )}
      </div>
    </div>
  );
}

export function AddressEditor({
  address,
  onDone,
}: {
  address?: Address;
  onDone: () => void;
}) {
  const [recipient, setRecipient] = useState(address?.recipient ?? "");
  const [phone, setPhone] = useState(address?.phone ?? "");
  const [value, setValue] = useState(address?.address ?? "");
  const [province, setProvince] = useState(address?.province ?? "");
  const [city, setCity] = useState(address?.city ?? "");
  const [region, setRegion] = useState(address?.region ?? "");
  const [postcode, setPostcode] = useState(address?.postcode ?? "");
  const [defaultValue, setDefaultValue] = useState(
    address?.is_default ?? false,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save() {
    setBusy(true);
    setError("");
    try {
      await api(`/invoice-addresses${address ? `/${address.id}` : ""}`, {
        method: address ? "PUT" : "POST",
        body: JSON.stringify({
          recipient: clean(recipient),
          phone: clean(phone),
          province: clean(province),
          city: clean(city),
          region: clean(region),
          address: clean(value),
          postcode: clean(postcode),
          is_default: defaultValue,
          expected_version: address?.version ?? 0,
        }),
      });
      onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="grid sm:grid-cols-2 gap-3">
      <div>
        <Label htmlFor="invoice-recipient">收件人</Label>
        <Input
          id="invoice-recipient"
          value={recipient}
          onChange={(e) => setRecipient(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-address-phone">电话</Label>
        <Input
          id="invoice-address-phone"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-province">省份</Label>
        <Input
          id="invoice-province"
          maxLength={80}
          value={province}
          onChange={(e) => setProvince(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-city">城市</Label>
        <Input
          id="invoice-city"
          maxLength={80}
          value={city}
          onChange={(e) => setCity(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-region">区县</Label>
        <Input
          id="invoice-region"
          maxLength={80}
          value={region}
          onChange={(e) => setRegion(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-postcode">邮编</Label>
        <Input
          id="invoice-postcode"
          maxLength={30}
          value={postcode}
          onChange={(e) => setPostcode(e.target.value)}
          disabled={busy}
        />
      </div>
      <div className="sm:col-span-2">
        <Label htmlFor="invoice-address">收货地址</Label>
        <Textarea
          id="invoice-address"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          disabled={busy}
        />
      </div>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={defaultValue}
          onCheckedChange={(v) => setDefaultValue(v === true)}
          disabled={busy}
        />
        设为默认地址
      </label>
      <div className="flex gap-2">
        <Button
          disabled={busy || !clean(recipient) || !clean(phone) || !clean(value)}
          onClick={() => void save()}
        >
          <Save />
          保存地址
        </Button>
        {error && (
          <p role="alert" className="text-sm text-destructive self-center">
            {error}
          </p>
        )}
      </div>
    </div>
  );
}
