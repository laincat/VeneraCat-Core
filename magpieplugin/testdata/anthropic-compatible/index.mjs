// The same shape as the OpenAI-compatible template, for a vendor that speaks the
// Anthropic Messages dialect. What changes is what a package declares, not how
// the host reads it:
//
//   dialect: "messages"   the host builds the body and reads the reply in that dialect
//   headers: { ... }      a vendor's own header names, e.g. x-api-key
//   (path defaults to /messages; declare `path` only when a vendor differs)
//
// Nothing here parses a reply: that is the host's job, which is why a package can
// be this short and still stream.

const DEFAULT_BASE = "https://api.anthropic.com/v1";

export async function AnthropicCompatiblePlugin(_input) {
  const provider = "anthropic-compatible";

  return {
    auth: {
      provider,
      methods: [
        {
          index: 0,
          label: "API Key",
          kind: "key",
          prompts: [
            {
              key: "baseURL",
              type: "text",
              message: "Base URL",
              placeholder: DEFAULT_BASE,
            },
            {
              key: "apiKey",
              type: "password",
              message: "API Key",
              placeholder: "sk-ant-…",
            },
          ],
          async authorize(inputs) {
            const key = String(inputs?.apiKey ?? "").trim();
            if (!key) {
              throw new Error("先填 API Key");
            }
            const base = String(inputs?.baseURL ?? DEFAULT_BASE).trim() || DEFAULT_BASE;
            return {
              type: "success",
              key,
              metadata: { baseURL: base.replace(/\/+$/, "") },
            };
          },
        },
      ],
      async loader(getAuth) {
        const auth = await getAuth();
        const base = auth?.metadata?.baseURL || DEFAULT_BASE;
        const key = typeof auth?.key === "string" ? auth.key : "";
        const headers = { "x-api-key": key, "anthropic-version": "2023-06-01" };
        const call = (url, init = {}) =>
          fetch(url, { ...init, headers: { ...headers, ...(init.headers || {}) } });
        return { baseURL: base, dialect: "messages", headers, fetch: call };
      },
    },
  };
}
