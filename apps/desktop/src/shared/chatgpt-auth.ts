export interface ChatGPTConnectRequest {
  providerId: string
  // Memoh session credential; OpenAI credentials never enter this interface.
  token: string
}
export type ChatGPTConnectResult = { ok: true } | { ok: false, code: string }
