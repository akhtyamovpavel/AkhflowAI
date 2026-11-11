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
  const raw = sessionStorage.getItem(key);
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
        const pending = readPending(storageKey);
        const acknowledged = sessionStorage.getItem(`${storageKey}.acknowledged`) === "1";
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
      const id = crypto.randomUUID();
      const pending = { id, text };
      sessionStorage.setItem(current.storageKey, JSON.stringify(pending));
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
      sessionStorage.removeItem(key);
      if (sessionStorage.getItem(key) !== null) throw new Error("storage removal failed");
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

  const markChecked = (id: string) => {
    setState((previous) => {
      if (previous.context !== latestContext.current || previous.pending?.id !== id) return previous;
      return { ...previous, checked: true };
    });
  };

  const acknowledge = (key: string, id: string) => {
    try {
      if (readPending(key)?.id !== id) return;
      sessionStorage.setItem(`${key}.acknowledged`, "1");
      if (sessionStorage.getItem(`${key}.acknowledged`) !== "1") throw new Error("storage write failed");
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

  return { state: current, remember, clear, markChecked, acknowledge };
}
