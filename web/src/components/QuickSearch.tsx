import { useState } from "react";
import { Input } from "@/components/ui/input";

type SearchItem = { id: number; title: string };

type QuickSearchProps = {
  tasks: SearchItem[];
  epics: SearchItem[];
  ready: boolean;
  onTaskOpen: (id: number) => void;
  onEpicOpen: (id: number) => void;
};

export function QuickSearch({ tasks, epics, ready, onTaskOpen, onEpicOpen }: QuickSearchProps) {
  const [query, setQuery] = useState("");
  const normalized = query.trim().toLocaleLowerCase("ru");
  const matchingTasks = tasks.filter((task) => task.title.toLocaleLowerCase("ru").includes(normalized));
  const matchingEpics = epics.filter((epic) => epic.title.toLocaleLowerCase("ru").includes(normalized));
  const resultCount = matchingTasks.length + matchingEpics.length;
  let resultMessage = `Найдено: ${resultCount}`;
  if (!ready) {
    resultMessage = "Загружаем данные проекта";
  } else if (resultCount === 0) {
    resultMessage = "Ничего не найдено";
  }

  function openTask(id: number) {
    setQuery("");
    onTaskOpen(id);
  }

  function openEpic(id: number) {
    setQuery("");
    onEpicOpen(id);
  }

  return (
    <section aria-label="Поиск по проекту" className="max-w-xl space-y-2">
      <label htmlFor="quick-search" className="text-sm font-medium">Поиск по задачам и эпикам</label>
      <Input
        id="quick-search"
        type="search"
        placeholder="Название задачи или эпика"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            setQuery("");
          }
        }}
      />
      {normalized && (
        <div className="rounded-md border bg-background p-3 text-sm">
          <p role="status" className="text-muted-foreground">
            {resultMessage}
          </p>
          {ready && matchingTasks.length > 0 && (
            <div className="mt-2">
              <h3 className="font-medium">Задачи</h3>
              <ul>
                {matchingTasks.map((task) => (
                  <li key={task.id}>
                    <button type="button" className="text-left hover:underline focus-visible:underline" onClick={() => openTask(task.id)}>
                      #{task.id} {task.title}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {ready && matchingEpics.length > 0 && (
            <div className="mt-2">
              <h3 className="font-medium">Эпики</h3>
              <ul>
                {matchingEpics.map((epic) => (
                  <li key={epic.id}>
                    <button type="button" className="text-left hover:underline focus-visible:underline" onClick={() => openEpic(epic.id)}>
                      {epic.title}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
