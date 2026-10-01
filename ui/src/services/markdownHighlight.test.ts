import { highlightCode, normalizeCodeLanguage } from "./markdownHighlight";

const cases: Array<[string | undefined, string | undefined]> = [
  ["js", "javascript"],
  [" JavaScript ", "javascript"],
  ["ts", "typescript"],
  ["py", "python"],
  ["sh", "shellscript"],
  ["shell", "shellscript"],
  ["rb", "ruby"],
  ["yml", "yaml"],
  ["c++", "cpp"],
  ["cs", "csharp"],
  ["md", "markdown"],
  ["tsx", "tsx"],
  ["  Elixir  ", "elixir"],
  ["hs", "haskell"],
  ["ZIG", "zig"],
  ["Dockerfile", "docker"],
  ["Nix", "nix"],
  ["clj", "clojure"],
  ["unknown", undefined],
  [undefined, undefined],
];

let failed = 0;
for (const [input, expected] of cases) {
  const actual = normalizeCodeLanguage(input);
  if (actual !== expected) {
    failed++;
    console.error(
      `FAIL: ${String(input)} normalized to ${String(actual)}, expected ${String(expected)}`,
    );
  }
}

if (failed > 0) process.exit(1);
console.log(`${cases.length} code-language aliases passed`);

// Embedded Shelley runs on the dashboard origin while Core serves its assets
// from another origin. The worker must be fetched with the session cookie and
// constructed from a page-origin blob URL.
Object.defineProperty(globalThis, "window", { value: globalThis, configurable: true });
Object.defineProperty(globalThis, "location", { value: new URL("http://localhost:3000"), configurable: true });
(globalThis as typeof globalThis & { __SHELLEY_INIT__: { base_url: string } }).__SHELLEY_INIT__ = {
  base_url: "http://localhost:8080",
};

let fetchedURL = "";
let fetchedCredentials: RequestCredentials | undefined;
let constructedURL = "";
globalThis.fetch = async (input, init) => {
  fetchedURL = String(input);
  fetchedCredentials = init?.credentials;
  return new Response("self.addEventListener('message', () => {})", {
    headers: { "Content-Type": "application/javascript" },
  });
};

class FakeWorker extends EventTarget {
  constructor(url: string | URL) {
    super();
    constructedURL = String(url);
  }

  postMessage(request: { id: number }) {
    queueMicrotask(() => this.dispatchEvent(new MessageEvent("message", {
      data: { id: request.id, kind: "unknown" },
    })));
  }
}
(globalThis as typeof globalThis & { Worker: typeof Worker }).Worker = FakeWorker as unknown as typeof Worker;

const result = await highlightCode("javascript", "const answer = 42;");
if (fetchedURL !== "http://localhost:8080/markdown-highlight-worker.js" ||
    fetchedCredentials !== "include" || !constructedURL.startsWith("blob:") ||
    result.kind !== "unknown") {
  throw new Error("Cross-origin markdown worker did not load through a credentialed blob URL");
}
