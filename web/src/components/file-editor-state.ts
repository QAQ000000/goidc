"use client";
import { useEffect } from "react";
export type EditorState = { busy: boolean; dirty: boolean };
export function useEditorState(busy: boolean, dirty: boolean, report: (state: EditorState) => void) {
  useEffect(() => { report({ busy, dirty }); return () => report({ busy: false, dirty: false }); }, [busy, dirty, report]);
}
export function canDiscard(state: EditorState) {
  return !state.busy && (!state.dirty || window.confirm("放弃未保存的修改？"));
}
