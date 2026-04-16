#!/usr/bin/env bun
// TRD channel plugin.
//
// This is the MCP server Claude Code connects to as a "channel." It does
// three things:
//   1. Reads .trd/config.json (path from $TRD_CONFIG) for its instance_id +
//      secret + dispatcher_port.
//   2. Opens a WebSocket to the local TRD dispatcher. Incoming Telegram
//      messages arrive as frames; they're forwarded to Claude as MCP
//      notifications so the active session sees them.
//   3. Exposes reply/react/edit_message/download_attachment tools; when
//      Claude calls them, they're serialized as frames back to the
//      dispatcher, which performs the actual Telegram API calls.

import { readFileSync } from "node:fs";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";

type RepoConfig = {
  instance_id: string;
  secret: string;
  dispatcher_port: number;
};

type InboundFrame = {
  type: "message";
  chat_id: number;
  message_id: number;
  thread_id: number;
  user?: string;
  text?: string;
  ts?: number;
  attachment_file_id?: string;
  attachment_name?: string;
};

type DownloadResultFrame = {
  type: "download_result";
  req_id: string;
  path?: string;
  error?: string;
};

type TTSResultFrame = {
  type: "tts_result";
  req_id: string;
  path?: string;
  error?: string;
};

type AnyInbound =
  | InboundFrame
  | DownloadResultFrame
  | TTSResultFrame
  | { type: string; [k: string]: unknown };

const CONFIG_PATH = process.env.TRD_CONFIG ?? process.env.CLAUDE_TRD_CONFIG;
if (!CONFIG_PATH) {
  console.error(
    "trd-channel: TRD_CONFIG (or CLAUDE_TRD_CONFIG) env var must point at .trd/config.json",
  );
  process.exit(2);
}

let cfg: RepoConfig;
try {
  cfg = JSON.parse(readFileSync(CONFIG_PATH, "utf8"));
} catch (e) {
  console.error(`trd-channel: failed to read ${CONFIG_PATH}:`, e);
  process.exit(2);
}

// Claude Code routes these notifications to the active session as
// <channel source="..." ...> context tags, but only if the server declares
// the `claude/channel` experimental capability AND publishes under the
// fully-qualified MCP method name. See claude-plugins-official/telegram
// server.ts for the reference implementation.
const NOTIFY_METHOD =
  process.env.TRD_NOTIFY_METHOD ?? "notifications/claude/channel";
const DISPATCHER = `ws://127.0.0.1:${cfg.dispatcher_port}/channel?secret=${encodeURIComponent(cfg.secret)}`;

let ws: WebSocket | null = null;
let backoffMs = 500;
const pendingDownloads = new Map<string, (f: DownloadResultFrame) => void>();
const pendingTTS = new Map<string, (f: TTSResultFrame) => void>();

const server = new Server(
  { name: "trd-channel", version: "0.1.0" },
  {
    capabilities: {
      tools: {},
      experimental: { "claude/channel": {} },
    },
    instructions: [
      "Messages from a Telegram topic arrive as claude/channel notifications.",
      "Each notification has `content` (the message text) and `meta` fields: chat_id, message_id, thread_id, user, ts, and optional attachment_file_id/attachment_name.",
      "To respond, call the reply tool and pass chat_id back. Omit reply_to for normal replies; only set it to quote a specific earlier message_id.",
      "To fetch an attachment, call download_attachment with attachment_file_id, then Read the returned path.",
      "Use react for emoji reactions, edit_message for in-progress updates (edits don't push-notify — send a fresh reply when a long task finishes).",
    ].join("\n"),
  },
);

function connect(): void {
  try {
    ws = new WebSocket(DISPATCHER);
  } catch (e) {
    console.error("trd-channel: ws ctor failed:", e);
    setTimeout(connect, backoffMs);
    backoffMs = Math.min(backoffMs * 2, 10_000);
    return;
  }
  ws.addEventListener("open", () => {
    backoffMs = 500;
    wsSend({ type: "hello", instance_id: cfg.instance_id });
  });
  ws.addEventListener("message", (ev) => {
    let frame: AnyInbound;
    try {
      frame = JSON.parse(String(ev.data));
    } catch (e) {
      console.error("trd-channel: bad json from dispatcher:", e);
      return;
    }
    onFrame(frame);
  });
  ws.addEventListener("close", () => {
    ws = null;
    setTimeout(connect, backoffMs);
    backoffMs = Math.min(backoffMs * 2, 10_000);
  });
  ws.addEventListener("error", (ev) => {
    console.error("trd-channel: ws error:", (ev as Event).type);
  });
}

function wsSend(obj: object): void {
  if (!ws || ws.readyState !== WebSocket.OPEN) {
    console.error(
      "trd-channel: drop frame, ws not open:",
      JSON.stringify(obj).slice(0, 200),
    );
    return;
  }
  ws.send(JSON.stringify(obj));
}

function onFrame(frame: AnyInbound): void {
  if (frame.type === "message") {
    const m = frame as InboundFrame;
    const tsIso = m.ts
      ? new Date(m.ts * 1000).toISOString()
      : new Date().toISOString();
    void server.notification({
      method: NOTIFY_METHOD,
      params: {
        content: m.text ?? "",
        meta: {
          source: "telegram",
          chat_id: m.chat_id,
          message_id: String(m.message_id),
          thread_id: String(m.thread_id),
          user: m.user ?? "",
          ts: tsIso,
          ...(m.attachment_file_id
            ? { attachment_file_id: m.attachment_file_id }
            : {}),
          ...(m.attachment_name
            ? { attachment_name: m.attachment_name }
            : {}),
        },
      },
    });
    return;
  }
  if (frame.type === "download_result") {
    const d = frame as DownloadResultFrame;
    const cb = pendingDownloads.get(d.req_id);
    if (cb) {
      pendingDownloads.delete(d.req_id);
      cb(d);
    }
    return;
  }
  if (frame.type === "tts_result") {
    const t = frame as TTSResultFrame;
    const cb = pendingTTS.get(t.req_id);
    if (cb) {
      pendingTTS.delete(t.req_id);
      cb(t);
    }
    return;
  }
  console.error("trd-channel: unknown frame:", frame.type);
}

server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    {
      name: "reply",
      description:
        "Send a text message to the Telegram topic this channel is bound to. " +
        "chat_id is the numeric Telegram chat ID from the incoming message. " +
        "reply_to is optional and quotes a specific message. " +
        "files is a list of absolute paths; each is sent as an attached document.",
      inputSchema: {
        type: "object",
        properties: {
          chat_id: { type: "number" },
          text: { type: "string" },
          reply_to: { type: "number" },
          files: { type: "array", items: { type: "string" } },
        },
        required: ["chat_id"],
      },
    },
    {
      name: "react",
      description: "Add an emoji reaction to a Telegram message.",
      inputSchema: {
        type: "object",
        properties: {
          chat_id: { type: "number" },
          message_id: { type: "number" },
          emoji: { type: "string" },
        },
        required: ["chat_id", "message_id", "emoji"],
      },
    },
    {
      name: "edit_message",
      description:
        "Edit the text of a message previously sent by this bot. Edits do not " +
        "re-notify the user — useful for in-progress status.",
      inputSchema: {
        type: "object",
        properties: {
          chat_id: { type: "number" },
          message_id: { type: "number" },
          text: { type: "string" },
        },
        required: ["chat_id", "message_id", "text"],
      },
    },
    {
      name: "download_attachment",
      description:
        "Download an incoming Telegram attachment by file_id. Returns an absolute " +
        "local path you can Read.",
      inputSchema: {
        type: "object",
        properties: { file_id: { type: "string" } },
        required: ["file_id"],
      },
    },
    {
      name: "send_voice",
      description:
        "Synthesize text to speech and send as a Telegram voice message. " +
        "Requires TRD_TTS_CMD (e.g. kokoro) or TRD_OPENAI_API_KEY on the dispatcher. " +
        "Returns an error if TTS is not configured.",
      inputSchema: {
        type: "object",
        properties: {
          text: { type: "string", description: "The text to speak" },
        },
        required: ["text"],
      },
    },
  ],
}));

server.setRequestHandler(CallToolRequestSchema, async (req) => {
  const { name, arguments: args = {} } = req.params;
  const a = args as Record<string, unknown>;
  switch (name) {
    case "reply": {
      wsSend({
        type: "reply",
        chat_id: Number(a.chat_id ?? 0),
        text: String(a.text ?? ""),
        reply_to: Number(a.reply_to ?? 0),
        files: Array.isArray(a.files) ? (a.files as string[]) : [],
      });
      return { content: [{ type: "text", text: "sent" }] };
    }
    case "react": {
      wsSend({
        type: "react",
        chat_id: Number(a.chat_id ?? 0),
        message_id: Number(a.message_id ?? 0),
        emoji: String(a.emoji ?? ""),
      });
      return { content: [{ type: "text", text: "reacted" }] };
    }
    case "edit_message": {
      wsSend({
        type: "edit",
        chat_id: Number(a.chat_id ?? 0),
        message_id: Number(a.message_id ?? 0),
        text: String(a.text ?? ""),
      });
      return { content: [{ type: "text", text: "edited" }] };
    }
    case "download_attachment": {
      const reqId = `dl-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
      const fileId = String(a.file_id ?? "");
      const result = await new Promise<DownloadResultFrame>((resolve, reject) => {
        const timer = setTimeout(() => {
          pendingDownloads.delete(reqId);
          reject(new Error("download timed out after 60s"));
        }, 60_000);
        pendingDownloads.set(reqId, (f) => {
          clearTimeout(timer);
          resolve(f);
        });
        wsSend({ type: "download", file_id: fileId, req_id: reqId });
      });
      if (result.error) {
        return {
          isError: true,
          content: [{ type: "text", text: `download failed: ${result.error}` }],
        };
      }
      return { content: [{ type: "text", text: result.path ?? "" }] };
    }
    case "send_voice": {
      const reqId = `tts-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
      const text = String(a.text ?? "");
      const result = await new Promise<TTSResultFrame>((resolve, reject) => {
        const timer = setTimeout(() => {
          pendingTTS.delete(reqId);
          reject(new Error("TTS timed out after 120s"));
        }, 120_000);
        pendingTTS.set(reqId, (f) => {
          clearTimeout(timer);
          resolve(f);
        });
        wsSend({ type: "tts", text, req_id: reqId });
      });
      if (result.error) {
        return {
          isError: true,
          content: [{ type: "text", text: `TTS failed: ${result.error}` }],
        };
      }
      return { content: [{ type: "text", text: `voice message sent` }] };
    }
    default:
      return {
        isError: true,
        content: [{ type: "text", text: `unknown tool ${name}` }],
      };
  }
});

connect();
const transport = new StdioServerTransport();
await server.connect(transport);

process.on("SIGINT", () => {
  try {
    ws?.close();
  } catch {
    /* ignore */
  }
  process.exit(0);
});
