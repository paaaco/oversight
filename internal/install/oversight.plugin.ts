// Written by `oversight install`. Reports OpenCode session events to the
// oversight dashboard by calling the oversight binary the same way Claude
// Code's hooks do. Re-run `oversight install` after moving the binary; this
// file is rewritten whenever the embedded path changes.
const OVERSIGHT = __OVERSIGHT__;

export const OversightPlugin = async ({ directory }) => {
  // Sub-sessions (OpenCode's subagents) are ignored, like Claude's agent_id.
  const subSessions = new Set();
  // Session id -> the SessionStart send, so later events wait for the file.
  const started = new Map();
  // User message ids already reported, and those whose text is still to come.
  const userMessages = new Set();
  const awaitingText = new Set();

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

  const start = (sid) => {
    const done = send("SessionStart", { session_id: sid, cwd: directory });
    started.set(sid, done);
    return done;
  };

  // Resumed sessions (opencode -c, --session, the picker) never fire
  // session.created, so the first event seen for a session starts it.
  const sendFor = async (sid, event, payload) => {
    if (!sid || subSessions.has(sid)) return;
    await (started.get(sid) ?? start(sid));
    send(event, payload);
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
          userMessages.add(info.id);
          awaitingText.add(info.id);
          // oversight keeps the previous prompt when this is empty; the text
          // follows in the message's first text part.
          await sendFor(info.sessionID, "UserPromptSubmit", { session_id: info.sessionID, prompt: "" });
          return;
        }
        case "message.part.updated": {
          const part = p.part ?? {};
          if (part.type !== "text" || part.synthetic || !awaitingText.has(part.messageID)) return;
          awaitingText.delete(part.messageID);
          await sendFor(part.sessionID, "UserPromptSubmit", { session_id: part.sessionID, prompt: part.text ?? "" });
          return;
        }
        case "permission.asked":
        case "question.asked": {
          const sid = p.sessionID ?? info.sessionID;
          await sendFor(sid, "Notification", { session_id: sid, notification_type: "permission_prompt" });
          return;
        }
        case "permission.replied":
        case "question.replied":
        case "question.rejected": {
          const sid = p.sessionID ?? info.sessionID;
          await sendFor(sid, "PostToolUse", { session_id: sid });
          return;
        }
        case "session.idle": {
          const sid = p.sessionID ?? info.id;
          await sendFor(sid, "Stop", { session_id: sid });
          return;
        }
        case "session.deleted": {
          const sid = info.id ?? p.sessionID;
          subSessions.delete(sid);
          started.delete(sid);
          send("SessionEnd", { session_id: sid });
          return;
        }
      }
    },
  };
};
