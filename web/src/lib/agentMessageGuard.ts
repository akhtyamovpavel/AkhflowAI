import { useEffect, useRef, useState } from "react";
import { reminderUserId } from "@/lib/deadlines";

type PendingMessage = {
  id: string;
  text: string;
};

type GuardState = {
  context: string;
  storageKey: string | null;
  ready: boolean;
  storageError: boolean;
  pending: PendingMessage | null;
  checked: boolean;
  acknowledged: boolean;
};

async function storageKeyFor(token: string, project: string, chatId: number): Promise<string> {
  let userId = reminderUserId(token);
  if (!userId) {
    const bytes = new TextEncoder().encode(token);
    const digest = await crypto.subtle.digest("SHA-256", bytes);
    userId = Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join("");
  }
  return JSON.stringify(["akhflow.pendingAgentMessage", userId, project, chatId]);
}

function readPending(key: string): PendingMessage | null {
  const raw = localStorage.getItem(key);
  if (raw === null) return null;
  const value: unknown = JSON.parse(raw);
  if (!value || typeof value !== "object" || !("id" in value) || !("text" in value)) {
    throw new Error("invalid pending message");
  }
  if (typeof value.id !== "string" || !value.id || typeof value.text !== "string") {
    throw new Error("invalid pending message");
  }
  return { id: value.id, text: value.text };
}

export function useAgentMessageGuard(token: string, project: string, chatId: number | null) {
  const context = JSON.stringify([token, project, chatId]);
  const latestContext = useRef(context);
  latestContext.current = context;
  const [state, setState] = useState<GuardState>({
    context: "",
    storageKey: null,
    ready: false,
    storageError: false,
    pending: null,
    checked: false,
    acknowledged: false,
  });
  const current = state.context === context ? state : null;

  useEffect(() => {
    let cancelled = false;
    if (!chatId || !project) return;
    void (async () => {
      try {
        const storageKey = await storageKeyFor(token, project, chatId);
        let pending = readPending(storageKey);
        const previous = sessionStorage.getItem(storageKey);
        if (!pending && previous !== null) {
          localStorage.setItem(storageKey, previous);
          pending = readPending(storageKey);
          sessionStorage.removeItem(storageKey);
        }
        const acknowledged = localStorage.getItem(`${storageKey}.acknowledged`) === "1";
        if (cancelled) return;
        setState({ context, storageKey, ready: true, storageError: false, pending, checked: false, acknowledged });
      } catch {
        if (cancelled) return;
        setState({ context, storageKey: null, ready: true, storageError: true, pending: null, checked: false, acknowledged: false });
      }
    })();
    return () => { cancelled = true; };
  }, [chatId, context, project, token]);

  const remember = (text: string): { key: string; id: string } | null => {
    if (!current?.ready || current.storageError || !current.storageKey || current.pending) return null;
    try {
      const existing = readPending(current.storageKey);
      if (existing) {
        setState({ ...current, pending: existing, checked: false });
        return null;
      }
      const bytes = crypto.getRandomValues(new Uint8Array(16));
      bytes[6] = (bytes[6] & 0x0f) | 0x40;
      bytes[8] = (bytes[8] & 0x3f) | 0x80;
      const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
      const id = `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
      const pending = { id, text };
      localStorage.setItem(current.storageKey, JSON.stringify(pending));
      if (readPending(current.storageKey)?.id !== id) throw new Error("storage write failed");
      setState({ ...current, pending, checked: false });
      return { key: current.storageKey, id };
    } catch {
      setState({ ...current, storageError: true });
      return null;
    }
  };

  const clear = (key: string, id: string): boolean => {
    try {
      if (readPending(key)?.id !== id) return false;
      localStorage.removeItem(key);
      if (localStorage.getItem(key) !== null) throw new Error("storage removal failed");
      setState((previous) => {
        if (previous.context !== latestContext.current || previous.storageKey !== key || previous.pending?.id !== id) {
          return previous;
        }
        return { ...previous, pending: null, checked: false };
      });
      return true;
    } catch {
      setState((previous) => {
        if (previous.context !== latestContext.current || previous.storageKey !== key) return previous;
        return { ...previous, storageError: true };
      });
      return false;
    }
  };

  const setChecked = (id: string, checked: boolean) => {
    setState((previous) => {
      if (previous.context !== latestContext.current || previous.pending?.id !== id) return previous;
      return { ...previous, checked };
    });
  };

  const acknowledge = (key: string, id: string) => {
    try {
      if (readPending(key)?.id !== id) return;
      localStorage.setItem(`${key}.acknowledged`, "1");
      if (localStorage.getItem(`${key}.acknowledged`) !== "1") throw new Error("storage write failed");
      if (clear(key, id)) {
        setState((previous) => previous.context === latestContext.current && previous.storageKey === key
          ? { ...previous, acknowledged: true }
          : previous);
      }
    } catch {
      setState((previous) => previous.context === latestContext.current && previous.storageKey === key
        ? { ...previous, storageError: true }
        : previous);
    }
  };

  return { state: current, remember, clear, setChecked, acknowledge };
}
