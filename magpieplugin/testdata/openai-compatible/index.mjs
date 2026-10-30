// A provider package that uses nothing but the Web APIs a client already has:
// fetch, Headers, URL, AbortSignal. It is the shape to copy when writing one.
//
// What Magpie's host reads from a package, and why each part exists:
//
//   auth.provider  the id an account is stored under; it must be stable
//   auth.methods   what a client shows on the sign-in screen
//   auth.loader    returns { baseURL, fetch }: the fetch that carries the sign-in
//   auth.usage     optional; absent means "this vendor reports no quota", not zero
//   auth.checkin   optional; absent means the vendor has no daily claim
//
// A package does not build request bodies or parse replies: the host does that,
// and vendor quirks stay in the package's own fetch.

const DEFAULT_BASE = "https://api.openai.com/v1";

export async function OpenAICompatiblePlugin(_input) {
  const provider = "openai-compatible";

  const method = {
    index: 0,
    label: "API Key",
    kind: "key",
    // The fields a client renders. `type: "password"` is what makes a field
    // secret; the host never needs to know which vendor this is.
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
        placeholder: "sk-…",
      },
    ],
    // A key settles the moment it is given: returning success here is what tells
    // the host there is no callback to poll.
    async authorize(inputs) {
      const key = String(inputs?.apiKey ?? "").trim();
      if (!key) {
        throw new Error("先填 API Key");
      }
      const base = String(inputs?.baseURL ?? DEFAULT_BASE).trim() || DEFAULT_BASE;
      return {
        type: "success",
        key,
        // Metadata is how a loader keeps what it needs between sessions: this
        // package stores its base URL here rather than in a global.
        metadata: { baseURL: base.replace(/\/+$/, "") },
      };
    },
  };

  return {
    auth: {
      provider,
      methods: [method],
      async loader(getAuth) {
        const auth = await getAuth();
        const base = auth?.metadata?.baseURL || DEFAULT_BASE;
        const key = typeof auth?.key === "string" ? auth.key : "";
        const call = (url, init = {}) => {
          const headers = { ...(init.headers || {}) };
          if (key) {
            headers.Authorization = `Bearer ${key}`;
          }
          return fetch(url, { ...init, headers });
        };
        return { baseURL: base, fetch: call };
      },
    },
  };
}
