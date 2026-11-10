type EventCallbacks = {
  onConnect: () => void;
  onEvent: (data: unknown) => void;
};

const MAX_EVENT_SIZE = 64 * 1024;
const MAX_RETRY_DELAY = 30_000;

async function readEvents(response: Response, signal: AbortSignal, onEvent: (data: unknown) => void) {
  if (!response.body) throw new Error("Missing event stream");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let data: string[] = [];
  let dataSize = 0;

  const line = (value: string) => {
    if (value === "") {
      if (data.length) {
        try {
          onEvent(JSON.parse(data.join("\n")));
        } catch {
          // Ignore malformed event data.
        }
      }
      data = [];
      dataSize = 0;
      return;
    }
    if (value.startsWith(":")) return;
    const separator = value.indexOf(":");
    const field = separator < 0 ? value : value.slice(0, separator);
    if (field !== "data") return;
    let content = separator < 0 ? "" : value.slice(separator + 1);
    if (content.startsWith(" ")) content = content.slice(1);
    dataSize += content.length + 1;
    if (dataSize > MAX_EVENT_SIZE) throw new Error("Event too large");
    data.push(content);
  };

  try {
    while (!signal.aborted) {
      const result = await reader.read();
      if (result.done) {
        if (buffer.endsWith("\r")) line(buffer.slice(0, -1));
        break;
      }
      buffer += decoder.decode(result.value, { stream: true });
      while (buffer.length) {
        const end = buffer.search(/[\r\n]/);
        if (end < 0 || (buffer[end] === "\r" && end === buffer.length - 1)) break;
        const width = buffer[end] === "\r" && buffer[end + 1] === "\n" ? 2 : 1;
        line(buffer.slice(0, end));
        buffer = buffer.slice(end + width);
      }
      if (buffer.length > MAX_EVENT_SIZE) throw new Error("Event line too large");
    }
  } finally {
    await reader.cancel().catch(() => {});
  }
}

export function subscribeProjectEvents(url: string, token: string, callbacks: EventCallbacks) {
  const controller = new AbortController();
  let retryTimer: number | undefined;
  let retryDelay = 1000;

  const connect = async () => {
    try {
      const response = await fetch(url, {
        headers: { Authorization: `Bearer ${token}`, Accept: "text/event-stream" },
        signal: controller.signal,
        credentials: "omit",
        redirect: "error",
        cache: "no-store",
      });
      if (response.status === 401 || response.status === 403) return;
      if (!response.ok || !response.headers.get("Content-Type")?.toLowerCase().startsWith("text/event-stream")) {
        throw new Error("Event stream unavailable");
      }
      if (controller.signal.aborted) return;
      callbacks.onConnect();
      await readEvents(response, controller.signal, (data) => {
        retryDelay = 1000;
        callbacks.onEvent(data);
      });
    } catch {
      // Retry transient failures; cancellation is handled below.
    }
    if (controller.signal.aborted) return;
    retryTimer = window.setTimeout(connect, retryDelay);
    retryDelay = Math.min(retryDelay * 2, MAX_RETRY_DELAY);
  };

  void connect();
  return () => {
    controller.abort();
    window.clearTimeout(retryTimer);
  };
}
