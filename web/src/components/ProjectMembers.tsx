import { useEffect, useRef, useState } from "react";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

type Request = (path: string, options?: RequestInit, token?: string | null) => Promise<any>;
type Member = { id: number; email: string; display_name: string; role: string };

export function ProjectMembers({ project, token, request, onMembersChanged }: { project: string; token: string; request: Request; onMembersChanged: () => Promise<void> }) {
  const [role, setRole] = useState("");
  const [members, setMembers] = useState<Member[]>([]);
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [selectedRole, setSelectedRole] = useState("viewer");
  const selectedRoleRef = useRef("viewer");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [verification, setVerification] = useState<"saved" | "unknown" | null>(null);
  const verificationRef = useRef<"saved" | "unknown" | null>(null);
  const submission = useRef<{ email: string; role: string } | null>(null);
  const active = useRef(false);
  const pending = useRef(false);
  const controllers = useRef(new Set<AbortController>());

  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      controllers.current.forEach((controller) => controller.abort());
      controllers.current.clear();
    };
  }, []);

  const current = () => active.current && localStorage.getItem("akhflow.token") === token;

  const updateVerification = (value: "saved" | "unknown" | null) => {
    verificationRef.current = value;
    setVerification(value);
  };

  const trackedRequest = async (path: string, options: RequestInit = {}) => {
    const controller = new AbortController();
    controllers.current.add(controller);
    try {
      return await request(path, { ...options, signal: controller.signal }, token);
    } finally {
      controllers.current.delete(controller);
    }
  };

  useEffect(() => {
    let active = true;
    request(`/api/projects/${project}/role`, {}, token)
      .then((result: { role: string }) => {
        if (active) setRole(result.role);
      })
      .catch(() => {
        if (active) setMessage("Не удалось загрузить права проекта");
      });
    return () => { active = false; };
  }, [project, token, request]);

  const loadMembers = async () => {
    const data: Member[] = await trackedRequest(`/api/projects/${project}/members`);
    if (current()) setMembers(data);
    return data;
  };

  const toggle = async () => {
    if (pending.current) return;
    if (open) {
      setOpen(false);
      return;
    }
    try {
      await loadMembers();
      if (current()) {
        setOpen(true);
        if (!verification) setMessage("");
      }
    } catch {
      if (current() && !verification) setMessage("Не удалось загрузить участников");
    }
  };

  const refresh = async (outcome: "saved" | "unknown") => {
    const results = await Promise.allSettled([loadMembers(), onMembersChanged()]);
    if (!current()) return;
    const local = results[0];
    const saved = outcome === "saved" || (local.status === "fulfilled" && submission.current &&
      local.value.some((member) => member.email === submission.current?.email && member.role === submission.current?.role));
    if (saved && submission.current) {
      const submittedEmail = submission.current.email;
      const submittedRole = submission.current.role;
      setEmail((value) => {
        if (value.trim() === submittedEmail && selectedRoleRef.current === submittedRole) return "";
        return value;
      });
    }
    if (results.some((result) => result.status === "rejected")) {
      updateVerification(saved ? "saved" : outcome);
      setMessage(saved
        ? "Права сохранены, но не удалось обновить список участников."
        : "Не удалось подтвердить сохранение прав. Проверь актуальный список участников.");
      return;
    }
    updateVerification(null);
    submission.current = null;
    setMessage(saved
      ? "Права участника сохранены"
      : "Сохранение не подтверждено. Проверь текущие права и повтори сохранение при необходимости.");
  };

  const retryRefresh = async () => {
    if (!verification || pending.current) return;
    pending.current = true;
    setBusy(true);
    try {
      await refresh(verification);
    } finally {
      pending.current = false;
      if (current()) setBusy(false);
    }
  };

  const save = async () => {
    if (!email.trim() || pending.current || verificationRef.current) return;
    pending.current = true;
    setBusy(true);
    setMessage("");
    const submitted = { email: email.trim(), role: selectedRole };
    submission.current = submitted;
    try {
      await trackedRequest(`/api/projects/${project}/members`, {
        method: "PUT",
        body: JSON.stringify(submitted),
      });
    } catch {
      if (current()) {
        updateVerification("unknown");
        setMessage("Не удалось подтвердить сохранение прав. Проверь актуальный список участников.");
      }
      pending.current = false;
      if (current()) setBusy(false);
      return;
    }
    if (current()) await refresh("saved");
    pending.current = false;
    if (current()) setBusy(false);
  };

  const canManage = role === "owner" || role === "admin";
  return (
    <section className="space-y-2" aria-label="Права проекта">
      {role === "viewer" && <p className="text-sm">Доступ к проекту: только просмотр.</p>}
      {canManage && (
        <>
          <Button variant="outline" size="sm" disabled={busy} onClick={toggle} aria-expanded={open}>Участники и роли</Button>
          {open && (
            <div className="max-w-xl space-y-2 rounded-md border p-3">
              <ul className="space-y-1 text-sm">
                {members.map((member) => <li key={member.id}>{member.display_name} ({member.email}) — {member.role}</li>)}
              </ul>
              <label htmlFor="member-email" className="text-sm">Email зарегистрированного пользователя</label>
              <Input id="member-email" type="email" value={email} onChange={(event) => setEmail(event.target.value)} />
              <label htmlFor="member-role" className="text-sm">Роль</label>
              <select id="member-role" value={selectedRole} onChange={(event) => {
                selectedRoleRef.current = event.target.value;
                setSelectedRole(event.target.value);
              }}>
                <option value="viewer">Просмотр</option>
                <option value="editor">Редактирование</option>
              </select>
              <Button size="sm" disabled={busy || !!verification || !email.trim()} onClick={save}>Сохранить права</Button>
            </div>
          )}
        </>
      )}
      {message && <p role="status" className="text-sm">{message}</p>}
      {verification && <Button variant="outline" size="sm" disabled={busy} onClick={retryRefresh}>Проверить список участников</Button>}
    </section>
  );
}
