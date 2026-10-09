// Written by `oversight install`. Reports OpenCode session events to the
// oversight dashboard by calling the oversight binary the same way Claude
// Code's hooks do. Re-run `oversight install` after moving the binary; this
// file is rewritten whenever the embedded path changes.
const OVERSIGHT = __OVERSIGHT__;

export const OversightPlugin = async ({ directory }) => {
  // Sub-sessions (OpenCode's subagents) are ignored, like Claude's agent_id.
  const subSessions = new Set();
  // Sessions SessionStart has been queued for.
  const started = new Set();
  // Session id -> the tail of its send chain. Hooks for one session run one
  // at a time, in event order, because each is a load-modify-save of the
  // session file; different sessions may overlap. Handlers do not wait on
  // the chain, so OpenCode is never held up by a hook.
  const chains = new Map();
  // User message id -> session id: messages already reported, and those
  // whose text is still to come.
  const userMessages = new Map();
  const awaitingText = new Map();

  const send = (event, payload) => {
    try {
      const proc = Bun.spawn(
        [OVERSIGHT, "hook", event, "--agent", "opencode", "--pid", String(process.pid)],
        { stdin: Buffer.from(JSON.stringify(payload)), stdout: "ignore", stderr: "ignore" },
      );
      proc.unref?.();
      return proc.exited.catch(() => {});
    } catch {
      // never let dashboard bookkeeping break the agent
      return Promise.resolve();
    }
  };

  const enqueue = (sid, event, payload) => {
    const next = (chains.get(sid) ?? Promise.resolve()).then(() => send(event, payload));
    chains.set(sid, next);
    return next;
  };

  const start = (sid) => {
    started.add(sid);
    return enqueue(sid, "SessionStart", { session_id: sid, cwd: directory });
  };

  // Resumed sessions (opencode -c, --session, the picker) never fire
  // session.created, so the first event seen for a session starts it.
  const sendFor = (sid, event, payload) => {
    if (!sid || subSessions.has(sid)) return;
    if (!started.has(sid)) start(sid);
    return enqueue(sid, event, payload);
  };

  const forget = (sid) => {
    subSessions.delete(sid);
    started.delete(sid);
    chains.delete(sid);
    for (const map of [userMessages, awaitingText]) {
      for (const [id, owner] of map) if (owner === sid) map.delete(id);
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
          if (!started.has(info.id)) start(info.id);
          return;
        }
        case "message.updated": {
          // Re-published for the same message after the turn; report it once.
          if (info.role !== "user" || userMessages.has(info.id)) return;
          if (subSessions.has(info.sessionID)) return;
          userMessages.set(info.id, info.sessionID);
          awaitingText.set(info.id, info.sessionID);
          // oversight keeps the previous prompt when this is empty; the text
          // follows in the message's first text part.
          sendFor(info.sessionID, "UserPromptSubmit", { session_id: info.sessionID, prompt: "" });
          return;
        }
        case "message.part.updated": {
          const part = p.part ?? {};
          if (part.type !== "text" || part.synthetic || !awaitingText.has(part.messageID)) return;
          awaitingText.delete(part.messageID);
          sendFor(part.sessionID, "UserPromptSubmit", { session_id: part.sessionID, prompt: part.text ?? "" });
          return;
        }
        case "permission.asked":
        case "question.asked": {
          const sid = p.sessionID ?? info.sessionID;
          sendFor(sid, "Notification", { session_id: sid, notification_type: "permission_prompt" });
          return;
        }
        case "permission.replied":
        case "question.replied":
        case "question.rejected": {
          const sid = p.sessionID ?? info.sessionID;
          sendFor(sid, "PostToolUse", { session_id: sid });
          return;
        }
        case "session.idle": {
          const sid = p.sessionID ?? info.id;
          sendFor(sid, "Stop", { session_id: sid });
          return;
        }
        case "session.deleted": {
          const sid = info.id ?? p.sessionID;
          enqueue(sid, "SessionEnd", { session_id: sid });
          forget(sid);
          return;
        }
      }
    },
  };
};
