"use client";

import type { ReactNode } from "react";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";

// Retry keys, validation and API mutations remain owned by each screen.
export function MutationDialog({
  open,
  busy,
  onClose,
  title,
  children,
  confirm,
  onConfirm,
  confirmDisabled,
  confirmVariant,
  showCloseButton = true,
}: {
  open: boolean;
  busy: boolean;
  onClose: () => void;
  title: ReactNode;
  children: ReactNode;
  confirm: ReactNode;
  onConfirm: () => void;
  confirmDisabled?: boolean;
  confirmVariant?: "default" | "destructive";
  showCloseButton?: boolean;
}) {
  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (!value && !busy) onClose();
      }}
    >
      <DialogContent showCloseButton={showCloseButton}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        {children}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            返回
          </Button>
          <Button
            variant={confirmVariant}
            disabled={busy || confirmDisabled}
            onClick={onConfirm}
          >
            {confirm}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
