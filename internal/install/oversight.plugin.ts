// Written by `oversight install`. Reports OpenCode session events to the
// oversight dashboard by calling the oversight binary the same way Claude
// Code's hooks do. Re-run `oversight install` after moving the binary; this
// file is rewritten whenever the embedded path changes.
const OVERSIGHT = __OVERSIGHT__;

export const OversightPlugin = async ({ directory }) => {
  // Sub-sessions (OpenCode's subagents) are ignored, like Claude's agent_id.
  const subSessions = new Set();

  const send = (event, payload) => {
    try {
      const proc = Bun.spawn(
        [OVERSIGHT, "hook", event, "--agent", "opencode", "--pid", String(process.pid)],
        { stdin: Buffer.from(JSON.stringify(payload)), stdout: "ignore", stderr: "ignore" },
      );
      proc.unref?.();
    } catch {
      // never let dashboard bookkeeping break the agent
    }
  };

  return {
    event: async ({ event }) => {
      const p = event?.properties ?? {};
      const info = p.info ?? {};
      switch (event?.type) {
        case "session.created": {
          if (info.parentID) {
            subSessions.add(info.id);
            return;
          }
          send("SessionStart", { session_id: info.id, cwd: directory });
          return;
        }
        case "message.updated": {
          if (info.role !== "user" || subSessions.has(info.sessionID)) return;
          // The prompt text is not reliably in this event; oversight keeps
          // the previous prompt when this is empty.
          send("UserPromptSubmit", { session_id: info.sessionID, prompt: "" });
          return;
        }
        case "permission.asked": {
          const sid = p.sessionID ?? info.sessionID;
          if (subSessions.has(sid)) return;
          send("Notification", { session_id: sid, notification_type: "permission_prompt" });
          return;
        }
        case "permission.replied": {
          const sid = p.sessionID ?? info.sessionID;
          if (subSessions.has(sid)) return;
          send("PostToolUse", { session_id: sid });
          return;
        }
        case "session.idle": {
          const sid = p.sessionID ?? info.id;
          if (subSessions.has(sid)) return;
          send("Stop", { session_id: sid });
          return;
        }
        case "session.deleted": {
          const sid = info.id ?? p.sessionID;
          subSessions.delete(sid);
          send("SessionEnd", { session_id: sid });
          return;
        }
      }
    },
  };
};
