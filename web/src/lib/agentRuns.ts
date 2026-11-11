export type AgentRun = {
  id: number;
  chat_id: number;
  actor_id: number;
  request_key: string;
  message: string;
  status: string;
  cancel_requested: boolean;
  step: { iteration: number; max_iterations: number; tool?: string | null };
  steps: { sequence: number; status: string; summary: string }[];
};

const statuses = new Set(["queued", "running", "succeeded", "step_limit", "failed", "canceled", "interrupted"]);

export function isAgentRunList(value: unknown, chatId: number): value is AgentRun[] {
  if (!Array.isArray(value)) return false;
  return value.every((item: unknown) => {
    if (!item || typeof item !== "object" || Array.isArray(item)) return false;
    const run = item as Record<string, unknown>;
    if (!Number.isSafeInteger(run.id) || (run.id as number) <= 0 || run.chat_id !== chatId) return false;
    if (!Number.isSafeInteger(run.actor_id) || (run.actor_id as number) <= 0) return false;
    if (typeof run.request_key !== "string" || !run.request_key || typeof run.message !== "string") return false;
    if (typeof run.status !== "string" || !statuses.has(run.status) || typeof run.cancel_requested !== "boolean") return false;
    if (!run.step || typeof run.step !== "object" || Array.isArray(run.step)) return false;
    const progress = run.step as Record<string, unknown>;
    if (!Number.isSafeInteger(progress.iteration) || !Number.isSafeInteger(progress.max_iterations)) return false;
    if (!Array.isArray(run.steps)) return false;
    return run.steps.every((entry: unknown) => {
      if (!entry || typeof entry !== "object" || Array.isArray(entry)) return false;
      const step = entry as Record<string, unknown>;
      return Number.isSafeInteger(step.sequence) && typeof step.status === "string" && typeof step.summary === "string";
    });
  });
}
