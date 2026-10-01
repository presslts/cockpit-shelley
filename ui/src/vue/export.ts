// Extract the /export/<id> conversation id from the current path.
export function exportConversationIdFromPath(): string | null {
  const m = window.location.pathname.match(/^(?:\/shelley\/[a-f0-9-]{36})?\/export\/([^/]+)\/?$/);
  return m ? decodeURIComponent(m[1]) : null;
}
