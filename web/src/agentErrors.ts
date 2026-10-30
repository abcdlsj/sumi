/** Human wording for a Run's stable error code, shared by every surface that reports one. */
export function runErrorLabel(code: string): string {
  const words = code.replaceAll(/[._]/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1) + ".";
}
