export type SharedTask = { project: string; taskId: number };

export function readSharedTask(location: Location): SharedTask | null {
  const params = new URL(location.href).searchParams;
  const project = params.get("task_project");
  const rawId = params.get("task");
  if (!project || !rawId || !/^[1-9]\d*$/.test(rawId)) return null;
  const taskId = Number(rawId);
  if (!Number.isSafeInteger(taskId)) return null;
  return { project, taskId };
}

export function telegramShareURL(location: Location, project: string, taskId: number, title: string): string {
  const taskURL = new URL(location.href);
  taskURL.username = "";
  taskURL.password = "";
  taskURL.search = "";
  taskURL.hash = "";
  taskURL.searchParams.set("task_project", project);
  taskURL.searchParams.set("task", String(taskId));
  const shareURL = new URL("https://t.me/share/url");
  shareURL.searchParams.set("url", taskURL.href);
  shareURL.searchParams.set("text", title);
  return shareURL.href;
}
