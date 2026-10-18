"use client";

import { createElement, useCallback, useEffect, useId, useRef, useState, type ComponentType, type KeyboardEvent, type MouseEvent } from "react";
import { fetchCaptcha, type CaptchaChallenge, type CaptchaPresentation, type CaptchaProof } from "@/lib/api";

export type CaptchaWidgetProps = {
  challenge: CaptchaPresentation;
  disabled: boolean;
  onToken: (token: string | null) => void;
  onError: (message: string) => void;
};

// Vendor adapters are imported and registered by application code at build time.
// API responses may select a registered name, but cannot supply executable URLs or HTML.
const widgets = new Map<string, ComponentType<CaptchaWidgetProps>>();
export function registerCaptchaWidget(name: string, component: ComponentType<CaptchaWidgetProps>) {
  if (!/^[a-z][a-z0-9_-]{0,63}$/.test(name) || widgets.has(name) || typeof component !== "function") throw new Error("验证码渲染器注册无效或重复");
  widgets.set(name, component);
}

const embeddedImage = /^data:image\/(?:png|jpeg|webp);base64,[A-Za-z0-9+/]+={0,2}$/;
const utf8Length = (value: string) => new TextEncoder().encode(value).length;
function dimensionsValid(value: number | undefined) { return value === undefined || (Number.isInteger(value) && value > 0 && value <= 4096); }
export function captchaPresentation(value: CaptchaChallenge): CaptchaPresentation {
  const challenge = value.challenge ?? (value.image ? { kind: "image", image: value.image } : undefined);
  if (!challenge || typeof challenge !== "object" || typeof challenge.prompt !== "undefined" && typeof challenge.prompt !== "string") throw new Error("验证码响应不完整，请刷新重试");
  if (utf8Length(JSON.stringify(challenge)) > 64 * 1024) throw new Error("验证码响应过大，请联系管理员");
  if (challenge.kind === "image" || challenge.kind === "click") {
    if (challenge.renderer && challenge.renderer !== challenge.kind) throw new Error("此验证码渲染器尚未接入，请联系管理员");
    if (typeof challenge.image !== "string" || !embeddedImage.test(challenge.image) || !dimensionsValid(challenge.width) || !dimensionsValid(challenge.height)) throw new Error("验证码图片响应无效，请刷新重试");
    if (challenge.kind === "click" && (!challenge.width || !challenge.height || !Number.isInteger(challenge.max_points) || challenge.max_points! < 1 || challenge.max_points! > 32)) throw new Error("点击验证码响应无效，请刷新重试");
  } else if (challenge.kind === "widget") {
    if (!challenge.renderer || !widgets.has(challenge.renderer)) throw new Error("此验证码渲染器尚未接入，请联系管理员");
    if (challenge.public_data !== undefined && (!challenge.public_data || typeof challenge.public_data !== "object" || Array.isArray(challenge.public_data))) throw new Error("验证码响应不完整，请刷新重试");
  } else throw new Error("此验证码类型尚未接入，请联系管理员");
  return challenge;
}

export function captchaProofReady(challenge: CaptchaPresentation, proof: CaptchaProof | null): boolean {
  if (!proof || proof.kind !== challenge.kind) return false;
  if (utf8Length(JSON.stringify(proof)) > 16 * 1024) return false;
  if (proof.kind === "image") return typeof proof.answer === "string" && !!proof.answer.trim() && utf8Length(proof.answer) <= 1024;
  if (proof.kind === "widget") return typeof proof.token === "string" && !!proof.token.trim() && utf8Length(proof.token) <= 12 * 1024;
  return Array.isArray(proof.points) && proof.points.length > 0 && proof.points.length <= Math.min(32, challenge.max_points ?? 0) && proof.points.every(point => point && Number.isFinite(point.x) && Number.isFinite(point.y) && point.x >= 0 && point.x <= 1 && point.y >= 0 && point.y <= 1);
}

type IssuedChallenge = CaptchaChallenge & { captcha_id: string; expires_at: string; challenge: CaptchaPresentation; action: "login" | "register" | "forgot-password" };
export function useCaptcha({ action, active, refreshSite }: { action: "login" | "register" | "forgot-password"; active: boolean; refreshSite: () => void }) {
  const [challenge, setChallenge] = useState<IssuedChallenge | null>(null);
  const [proof, setProof] = useState<CaptchaProof | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<AbortController | null>(null);
  const currentChallenge = useRef<IssuedChallenge | null>(null);
  const refresh = useCallback(async () => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    currentChallenge.current = null;
    setChallenge(null); setProof(null); setError(""); setBusy(true);
    try {
      const next = await fetchCaptcha(action, controller.signal);
      if (controller.signal.aborted) return;
      if (next?.enabled === false) { refreshSite(); return; }
      if (next?.enabled !== true || typeof next.captcha_id !== "string" || !next.captcha_id || typeof next.expires_at !== "string" || !Number.isFinite(Date.parse(next.expires_at)) || Date.parse(next.expires_at) <= Date.now()) throw new Error("验证码响应已失效，请刷新重试");
      const issued = { ...next, action, captcha_id: next.captcha_id, expires_at: next.expires_at, challenge: captchaPresentation(next) };
      currentChallenge.current = issued;
      setChallenge(issued);
    } catch (failure) {
      if (!controller.signal.aborted) setError((failure as Error).message || "验证码暂时不可用，请重试");
    } finally { if (!controller.signal.aborted) setBusy(false); }
  }, [action, refreshSite]);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      if (active) void refresh();
      else { request.current?.abort(); currentChallenge.current = null; setChallenge(null); setProof(null); setError(""); setBusy(false); }
    }, 0);
    return () => { window.clearTimeout(timer); request.current?.abort(); currentChallenge.current = null; };
  }, [active, refresh]);
  useEffect(() => {
    if (!challenge) return;
    const timer = window.setTimeout(() => { currentChallenge.current = null; setProof(null); setChallenge(null); setError("验证码已过期，请刷新后重试"); }, Math.max(0, Date.parse(challenge.expires_at) - Date.now()));
    return () => window.clearTimeout(timer);
  }, [challenge]);
  const visibleChallenge = active && challenge?.action === action ? challenge : null;
  const ready = !active || !!(!busy && !error && visibleChallenge && captchaProofReady(visibleChallenge.challenge, proof));
  const canSubmit = () => ready && (!active || !!challenge && Date.parse(challenge.expires_at) > Date.now());
  const fields = visibleChallenge ? { captcha_id: visibleChallenge.captcha_id, captcha_proof: proof, ...(proof?.kind === "image" ? { captcha_answer: proof.answer } : {}) } : {};
  const updateProof = (next: CaptchaProof | null) => { if (active && challenge && currentChallenge.current?.captcha_id === challenge.captcha_id) setProof(next); };
  const fail = (message: string) => { if (active && challenge && currentChallenge.current?.captcha_id === challenge.captcha_id) { setProof(null); setError(message || "验证码暂时不可用，请刷新重试"); } };
  return { challenge: visibleChallenge, proof: visibleChallenge ? proof : null, busy: active && busy, error: active ? error : "", refresh, ready, canSubmit, fields, setProof: updateProof, fail };
}

export type CaptchaController = ReturnType<typeof useCaptcha>;
function ClickCaptcha({ challenge, proof, onProof, disabled }: { challenge: CaptchaPresentation; proof: CaptchaProof | null; onProof: (proof: CaptchaProof | null) => void; disabled: boolean }) {
  const [cursor, setCursor] = useState({ x: 0.5, y: 0.5 });
  const points = proof?.kind === "click" ? proof.points : [];
  const addPoint = (point: { x: number; y: number }) => {
    if (disabled || points.length >= challenge.max_points! || !Number.isFinite(point.x) || !Number.isFinite(point.y) || point.x < 0 || point.x > 1 || point.y < 0 || point.y > 1) return;
    onProof({ kind: "click", points: [...points, point] });
  };
  const click = (event: MouseEvent<HTMLButtonElement>) => {
    if (!event.detail) { addPoint(cursor); return; }
    const rect = event.currentTarget.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    addPoint({ x: (event.clientX - rect.left) / rect.width, y: (event.clientY - rect.top) / rect.height });
  };
  const keyboard = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Enter", " "].includes(event.key)) event.preventDefault();
    if (event.key === "Enter" || event.key === " ") { addPoint(cursor); return; }
    const step = event.shiftKey ? 0.01 : 0.05;
    if (event.key.startsWith("Arrow")) setCursor(previous => ({ x: Math.min(1, Math.max(0, previous.x + (event.key === "ArrowLeft" ? -step : event.key === "ArrowRight" ? step : 0))), y: Math.min(1, Math.max(0, previous.y + (event.key === "ArrowUp" ? -step : event.key === "ArrowDown" ? step : 0))) }));
  };
  return (
    <div className="space-y-2">
      <p className="text-xs text-muted-foreground">按顺序点击图片；键盘方向键移动光标，Shift 精细移动，回车或空格选择。</p>
      <button type="button" aria-label="点击验证码图片" disabled={disabled} onClick={click} onKeyDown={keyboard} className="relative block max-w-full overflow-hidden border-0 p-0 focus-visible:outline-2 focus-visible:outline-primary" style={{ width: challenge.width }}>
        <img src={challenge.image} alt="点击验证码" width={challenge.width} height={challenge.height} draggable={false} className="block h-auto w-full" />
        <span aria-hidden="true" className="absolute h-3 w-3 rounded-full border-2 border-white bg-black/60" style={{ left: `${cursor.x * 100}%`, top: `${cursor.y * 100}%`, transform: "translate(-50%, -50%)" }} />
        {points.map((point, i) => <span key={i} aria-hidden="true" className="absolute flex h-5 w-5 items-center justify-center rounded-full bg-primary text-xs text-white" style={{ left: `${point.x * 100}%`, top: `${point.y * 100}%`, transform: "translate(-50%, -50%)" }}>{i + 1}</span>)}
      </button>
      <p role="status" className="sr-only">光标位置：横向 {Math.round(cursor.x * 100)}%，纵向 {Math.round(cursor.y * 100)}%。</p>
      <div className="flex items-center gap-3 text-sm">
        <span role="status">已选择 {points.length} 个点，最多 {challenge.max_points} 个</span>
        <button type="button" disabled={disabled || !points.length} className="underline" onClick={() => onProof(null)}>重新选择</button>
      </div>
    </div>
  );
}

export function CaptchaField({ captcha, disabled = false }: { captcha: CaptchaController; disabled?: boolean }) {
  const id = useId();
  const issued = captcha.challenge;
  const challenge = issued?.challenge;
  const blocked = disabled || captcha.busy || !!captcha.error;
  const Widget = challenge?.kind === "widget" && challenge.renderer ? widgets.get(challenge.renderer) : undefined;
  return (
    <fieldset className="space-y-2" disabled={disabled}>
      <legend className="text-sm font-medium">安全验证</legend>
      {captcha.busy && <p role="status" className="text-sm text-muted-foreground">正在加载验证码…</p>}
      {captcha.error && <p role="alert" className="text-sm text-destructive">{captcha.error}</p>}
      {challenge?.prompt && <p className="text-sm">{challenge.prompt}</p>}
      {challenge?.kind === "image" && <>
        <img src={challenge.image} alt="图形验证码" width={challenge.width} height={challenge.height} className="max-w-full border bg-slate-50" />
        <label htmlFor={id} className="block text-sm">图片中的验证码</label>
        <input id={id} value={captcha.proof?.kind === "image" ? captcha.proof.answer : ""} onChange={event => captcha.setProof({ kind: "image", answer: event.target.value })} disabled={blocked} autoComplete="off" autoCapitalize="none" spellCheck={false} maxLength={1024} required className="h-10 w-full rounded-md border px-3 text-sm" />
      </>}
      {issued && challenge?.kind === "click" && <ClickCaptcha key={issued.captcha_id} challenge={challenge} proof={captcha.proof} onProof={captcha.setProof} disabled={blocked} />}
      {issued && challenge?.kind === "widget" && Widget && createElement(Widget, { key: issued.captcha_id, challenge, disabled: blocked, onToken: token => captcha.setProof(token ? { kind: "widget", token } : null), onError: captcha.fail })}
      <button type="button" className="text-sm underline disabled:opacity-50" disabled={disabled || captcha.busy} onClick={() => void captcha.refresh()}>刷新验证码</button>
    </fieldset>
  );
}
