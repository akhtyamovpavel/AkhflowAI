export type DeadlineTask = {
  id: number;
  title: string;
  status: string;
  due_date?: string;
  recurring?: string;
};

function calendarDate(date: Date): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function deadlineDate(task: DeadlineTask): string {
  return task.due_date?.slice(0, 10) ?? "";
}

export function upcomingDeadlines(tasks: DeadlineTask[], now = new Date()): DeadlineTask[] {
  const today = calendarDate(now);
  const nextDay = new Date(now);
  nextDay.setDate(nextDay.getDate() + 1);
  const tomorrow = calendarDate(nextDay);
  return tasks.filter((task) => {
    const due = deadlineDate(task);
    return task.status !== "done" && !task.recurring && (due === today || due === tomorrow);
  }).sort((a, b) => deadlineDate(a).localeCompare(deadlineDate(b)) || a.id - b.id);
}

export function deadlineMessage(task: DeadlineTask, now = new Date()): string {
  if (deadlineDate(task) === calendarDate(now)) {
    return "Срок сегодня";
  }
  return "Срок завтра";
}

// The subject is used only for local deduplication; API access is authorized by the server.
export function reminderUserId(token: string): string | null {
  try {
    const parts = token.split(".");
    if (parts.length !== 3) return null;
    const base64 = parts[1].replace(/-/g, "+").replace(/_/g, "/");
    const claims: unknown = JSON.parse(atob(base64));
    if (!claims || typeof claims !== "object" || !("sub" in claims)) return null;
    const subject = claims.sub;
    return typeof subject === "string" && /^[1-9][0-9]*$/.test(subject) ? subject : null;
  } catch {
    return null;
  }
}

export function reminderKey(userId: string, project: string, task: DeadlineTask): string {
  return `akhflow.deadline.v2.${userId}.${encodeURIComponent(project)}.${task.id}.${deadlineDate(task)}`;
}
